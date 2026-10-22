//go:build !linux && !windows && !windows

package proxy

func readCapacitySample() capacitySample {
	return capacitySample{reason: "no-calibrated-native-fd-sensor"}
}
