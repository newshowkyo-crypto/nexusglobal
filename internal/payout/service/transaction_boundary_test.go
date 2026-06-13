package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreezeTransactionBoundary_NoExternalCalls(t *testing.T) {
	// This test verifies that the Freeze function's transaction body
	// does not contain any external HTTP calls or external client calls.
	// This is a static analysis test to enforce the transaction boundary rule:
	// "DB 本地事务内禁止任何外部网络 I/O"

	repoRoot := findRepoRoot(t)
	serviceFile := filepath.Join(repoRoot, "internal", "payout", "service", "service.go")

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, serviceFile, nil, parser.ParseComments)
	require.NoError(t, err)

	var freezeFunc *ast.FuncDecl
	for _, decl := range node.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				if fn.Name.Name == "Freeze" {
					freezeFunc = fn
					break
				}
			}
		}
	}
	require.NotNil(t, freezeFunc, "Freeze function not found")

	// 找到 WithinTx 闭包
	var withinTxClosure *ast.FuncLit
	ast.Inspect(freezeFunc, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// 检查是否是 WithinTx 调用
		if sel, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "WithinTx" {
				// 第二个参数是闭包
				if len(callExpr.Args) >= 2 {
					if funcLit, ok := callExpr.Args[1].(*ast.FuncLit); ok {
						withinTxClosure = funcLit
					}
				}
			}
		}

		return true
	})

	require.NotNil(t, withinTxClosure, "WithinTx closure not found")

	// 检查闭包内的所有调用
	externalCalls := []string{}
	ast.Inspect(withinTxClosure, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		callStr := formatCallExpr(callExpr)
		if isExternalCall(callStr) {
			externalCalls = append(externalCalls, callStr)
		}

		return true
	})

	assert.Empty(t, externalCalls, "Freeze transaction body must not contain external calls: %v", externalCalls)
}

func formatCallExpr(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

func isExternalCall(callStr string) bool {
	// 检测对 net/http 包的任何调用
	if strings.HasPrefix(callStr, "http.") {
		return true
	}

	// 检测外部 Client 接口方法调用 (LP/Bank/MAE 相关)
	// 只匹配完整的方法名，避免误匹配数据库操作
	externalMethods := []string{
		".FXLock", ".Payout", ".QueryStatus",
		".SubmitTransfer", ".QueryTransfer",
		".HealthCheck", ".CreatePayment",
	}

	for _, method := range externalMethods {
		if strings.Contains(callStr, method) {
			return true
		}
	}

	// 检测对任何外部 HTTP 请求构造
	if strings.Contains(callStr, "NewRequest") && strings.Contains(callStr, "http") {
		return true
	}

	return false
}

func findRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}
