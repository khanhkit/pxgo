package proxy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The real-AD fixture is intentionally unavailable on public runners. Keep
// its public CI logs free of domain identities even when the protected
// environment's Kerberos handshake fails.
func TestRealADPublicLogRedactionContract(t *testing.T) {
	const source = "sspi_windows_integration_test.go"
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, proof := range []string{
		`exec.Command("whoami", "/upn")`,
		`exec.Command("klist", "get", spn)`,
		`strings.ToLower(string(klistOut)), strings.ToLower(spn)`,
		`bytes.Contains(initialToken, kerberosOID)`,
		`strings.TrimSpace(username) == ""`,
		`!managedSSPISessionComplete(session)`,
		`containsNTLMSSP(initialToken)`,
	} {
		if !strings.Contains(text, proof) {
			t.Errorf("protected AD authentication evidence check missing: %s", proof)
		}
	}

	file, err := parser.ParseFile(token.NewFileSet(), source, contents, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	private := map[string]bool{
		"host": true, "upn": true, "upnOut": true,
		"spn": true, "klistOut": true, "tickets": true,
		"username": true, "initialToken": true, "toClient": true, "toServer": true,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || (fn.Name.Name != "requireRealADSSPIFixture" &&
			fn.Name.Name != "TestTCSSPIWINAD010RealADNegotiateUsesKerberos") {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := method.X.(*ast.Ident)
			if !ok || receiver.Name != "t" || !strings.Contains("|Log|Logf|Fatal|Fatalf|Error|Errorf|", "|"+method.Sel.Name+"|") {
				return true
			}
			for _, arg := range call.Args {
				ast.Inspect(arg, func(node ast.Node) bool {
					id, ok := node.(*ast.Ident)
					if ok && private[id.Name] {
						t.Errorf("public real-AD test log includes sensitive value %s in %s", id.Name, method.Sel.Name)
					}
					return true
				})
			}
			return true
		})
	}
}
