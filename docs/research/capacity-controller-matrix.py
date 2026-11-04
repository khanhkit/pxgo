#!/usr/bin/env python3
"""Deterministic *synthetic* resource-envelope admission-control benchmark.
This does not measure PxGo/Go/Windows performance. Test controller proposals
under identical abstract demand, changing safe capacity and sensor failures.
"""
import argparse
import csv
import itertools
import json
import math
import random
from pathlib import Path

DT = 0.25
N = int(75 / DT)
SEEDS = range(6)
CONTROLLERS = ('fixed256','r11_aimd','linear_aimd','hpa','pi','pid','guarded_pi')
SCENARIOS = ('step_2560','pulse_oscillation','resource_cliff','sensor_gap','mixed_ramp','idle_recovery')

# Units are downstream connections, seconds, and abstract resource capacity.
def workload(kind, t, rnd):
    if kind == 'step_2560': return (256 if t < 3 else 2560), 4096, True
    if kind == 'pulse_oscillation': return (2560 if int(t / 7) % 2 else 350), 4096, True
    if kind == 'resource_cliff': return 2560, (4096 if t < 18 or t > 37 else 1024), True
    if kind == 'sensor_gap': return 2560, 4096, not(12 <= t <= 27)
    if kind == 'mixed_ramp':
        d = 256 + min(2600, int(max(0,t-2)*105))
        return max(16,d + rnd.randint(-60,60)), 4096, True
    if kind == 'idle_recovery': return (2560 if 5 < t < 26 else 100), 4096, True
    raise ValueError(kind)

class Governor:
    def __init__(self,kind,params):
        self.kind=kind; self.p=params; self.soft=256.0
        self.prev=0.; self.integral=0.; self.derivative=0.
        self.healthy=0; self.pressured=0; self.lastgrow=-999.; self.lastshrink=-999.; self.lastchange=-999.
    def tick(self, t, active, demand, hard, sensor_ok, safety_pressure):
        if self.kind=='fixed256': return 256.0
        if sensor_ok and not safety_pressure:self.healthy+=1; self.pressured=0
        elif sensor_ok:self.pressured+=1; self.healthy=0
        else:self.healthy=0;self.pressured=0
        # No controller is permitted to grow on missing/unhealthy telemetry.
        if sensor_ok and safety_pressure and self.pressured>=2 and t-self.lastshrink>=3:
            self.soft=max(1.,self.soft/2); self.lastshrink=t;self.integral=0.
            return self.soft
        if not sensor_ok or self.healthy<3 or safety_pressure:return self.soft
        if self.kind in ('r11_aimd','linear_aimd'):
            if (active >= min(self.soft,hard)-1 or demand>active+1) and t-self.lastgrow>=0.5:
                step = max(32.,min(self.soft/2,512.)) if self.kind=='r11_aimd' else 32.
                self.soft+=step;self.lastgrow=t
        elif self.kind=='hpa':
            # Scaled down to admission quotas, not to pod count; deadband 10%.
            desired=min(hard,demand)
            ratio=desired/max(1.,self.soft)
            if abs(ratio-1)>0.1 and t-self.lastchange>=0.5:
                self.soft += max(-512.,min(512.,self.soft*(ratio-1)))
                self.lastchange=t
        else:
            # PI/PID variants control demand shortfall, never cgroup/FD limits.
            target=min(demand,hard)
            error=target-self.soft
            kp,ki,kd=self.p
            # Derivative on smoothed *measurement* avoids setpoint kick.
            d_meas=(active-self.prev)/DT
            self.derivative = 0.75*self.derivative+0.25*d_meas
            self.prev=active
            if self.kind=='guarded_pi':
                # Demand feedforward + slow PI trim, finite slew budget.
                delta=0.30*max(0.,error) + kp*error*0.20 + ki*self.integral
                max_up=512.;max_down=256.
            else:
                # Discrete positional PID, with clipping and conditional integration.
                delta=kp*error + ki*self.integral - kd*self.derivative
                max_up=512.;max_down=512.
            raw=max(-max_down,min(max_up,delta))
            projected=self.soft+raw
            saturated=(projected>=hard and error>0) or (projected<=1 and error<0)
            if not saturated and abs(delta-raw)<1e-9:
                self.integral=max(-3000.,min(3000.,self.integral+error*DT))
            self.soft+=raw
        self.soft=max(1.,min(4096.,self.soft))
        return self.soft

def simulate(kind,scenario,seed,params,keep_trace=False,observability='oracle'):
    rnd=random.Random(10000+seed)
    gov=Governor(kind,params)
    active=0.
    backlog_area=0.; above_cap_area=0.; churn=0.; rejects=0.; safety_violation=0
    last=gov.soft; max_queue=0.; max_active=0.;max_soft=0.; first_2560=None; recover=None
    trace=[]
    for k in range(N):
        t=k*DT
        demand,hard,sensor_ok=workload(scenario,t,rnd)
        # A fall in true resource capacity cannot forcibly evict useful tunnels.
        # Old sockets naturally drain; newly admitted sockets always obey cap.
        safety_pressure=sensor_ok and hard <= active + 8
        # Under PxGo's serialized accept-before-acquire path, only a tiny
        # number of application waiters is visible even if 2,560 SYNs queue.
        # The oracle model is *not* achievable with current instrumentation.
        observed_demand=demand
        if observability=='serial_accept':
            observed_demand=active+1 if demand>active and active>=min(gov.soft,hard)-1 else active
        soft=gov.tick(t,active,observed_demand,hard,sensor_ok,safety_pressure)
        effective=min(hard,soft) if sensor_ok else min(hard,last)
        # All algorithms must honor real safety cap even when telemetry misses.
        # This is an independent safety oracle, not an implementation claim.
        if not sensor_ok:effective=min(effective,soft)
        target=min(effective,demand)
        if target>active:
            # Per-quarter-second dial/CONNECT admission arrival envelope.
            active+=min(target-active,220.)
        else:
            active-=min(active-target,40.)
        backlog=max(0.,demand-active)
        max_queue=max(max_queue,backlog)
        backlog_area+=backlog*DT
        above_cap_area+=max(0.,active-hard)*DT
        churn+=abs(soft-last)
        max_active=max(active,max_active);max_soft=max(soft,max_soft)
        # Control-level safety invariant: no *new* admitted socket beyond hard.
        if active>hard+1e-6 and target>active:safety_violation+=1
        if scenario in ('step_2560','sensor_gap') and active>=2559.5 and first_2560 is None:first_2560=t
        if scenario=='resource_cliff' and t>=37 and active>=2500 and recover is None:recover=t-37
        if keep_trace and k%2==0:trace.append(dict(t=round(t,2),kind=kind,scenario=scenario,seed=seed,demand=demand,hard=hard,sensor=int(sensor_ok),active=round(active,2),soft=round(soft,2),backlog=round(backlog,2)))
        last=soft
    return dict(controller=kind,scenario=scenario,seed=seed,params='|'.join(map(str,params)),backlog_area=round(backlog_area,1),max_backlog=round(max_queue),above_cap_area=round(above_cap_area,1),churn=round(churn),max_active=round(max_active),max_soft=round(max_soft),reach_2560_s=round(first_2560-3,2) if first_2560 is not None and scenario=='step_2560' else '',recovery_s=round(recover,2) if recover is not None else '',simulated_safety_violation=safety_violation),trace

def ranked_score(results):
    # Domain objectives: demand wait 60%; repeated actuator movement 10%;
    # inability to restore after memory headroom returns 30%; no safety violations.
    step=[r for r in results if r['scenario']=='step_2560']
    cliff=[r for r in results if r['scenario']=='resource_cliff']
    pulse=[r for r in results if r['scenario']=='pulse_oscillation']
    return round(sum(r['backlog_area'] for r in step)/max(1,len(step)) + .3*sum(r['backlog_area'] for r in cliff)/max(1,len(cliff)) + .08*sum(r['churn'] for r in pulse)/max(1,len(pulse)),2)

def main():
    args=argparse.ArgumentParser();args.add_argument('--out',default='results');args.add_argument('--observability',choices=['oracle','serial_accept'],default='oracle');ns=args.parse_args()
    out=Path(ns.out);out.mkdir(parents=True,exist_ok=True)
    # Each candidate evaluated on same six scenarios and six deterministic seeds.
    gains={'fixed256':(0,0,0),'r11_aimd':(0,0,0),'linear_aimd':(0,0,0),'hpa':(0,0,0),
           'pi':(0.28,0.04,0),'pid':(0.25,0.04,0.12),'guarded_pi':(0.18,0.025,0)}
    allrows=[];trace=[]
    for name,scenario,seed in itertools.product(CONTROLLERS,SCENARIOS,SEEDS):
        r,tr=simulate(name,scenario,seed,gains[name],keep_trace=(seed==0 and scenario in ('step_2560','resource_cliff','sensor_gap')),observability=ns.observability)
        allrows.append(r);trace+=tr
    sweeps=[]
    for kind in ('pi','pid','guarded_pi'):
        for kp,ki,kd in itertools.product((0.1,0.2,0.35,0.55),(0.0,0.02,0.07),(0.0,0.12) if kind=='pid' else (0.0,)):
            pars=(kp,ki,kd)
            rs=[simulate(kind,s,seed,pars,observability=ns.observability)[0] for s in SCENARIOS for seed in range(3)]
            sweeps.append(dict(controller=kind,kp=kp,ki=ki,kd=kd,score=ranked_score(rs),worst_above_cap=max(x['above_cap_area'] for x in rs)))
    for fname,rows in [('controller_scenarios.csv',allrows),('controller_trace.csv',trace),('pid_gain_sweep.csv',sweeps)]:
        with (out/fname).open('w',newline='') as f:
            w=csv.DictWriter(f,fieldnames=list(rows[0]));w.writeheader();w.writerows(rows)
    summary=[]
    for name in CONTROLLERS:
        xs=[r for r in allrows if r['controller']==name]
        n=len(xs)
        summary.append(dict(controller=name,score=ranked_score(xs),avg_backlog_integral=round(sum(r['backlog_area'] for r in xs)/n),avg_controller_churn=round(sum(r['churn'] for r in xs)/n),avg_step_2560_s=round(sum(float(r['reach_2560_s']) for r in xs if r['reach_2560_s']!='')/6,2) if len([r for r in xs if r['reach_2560_s']!=''])==6 else None,max_above_cap_integral=max(r['above_cap_area'] for r in xs),max_simulated_violations=max(r['simulated_safety_violation'] for r in xs)))
    summary.sort(key=lambda r:r['score'])
    data=dict(experiment='synthetic controller comparison, NOT live PxGo network benchmark',observability=ns.observability,dt=DT,horizon=75,seeds=len(SEEDS),scenarios=SCENARIOS,metric_warnings=['backlog_integral in connection-seconds, not measured TCP latency','simulated resource cliff allows existing healthy sockets to drain; above-cap usage is not a forced-eviction violation','simulator uses perfect safety oracle; real telemetry stale/unknown behavior requires production hard gates','score weights are explicitly hypothetical, not user acceptance thresholds'],summary=summary,top_pid_sweep=sorted(sweeps,key=lambda r:r['score'])[:12])
    (out/'summary.json').write_text(json.dumps(data,indent=2)+'\n')
    print(json.dumps(data,indent=2))
if __name__=='__main__':main()
