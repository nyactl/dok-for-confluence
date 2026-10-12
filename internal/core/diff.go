package core

import (
	"fmt"
	"strings"
)

const (
	diffContext  = 2
	maxDiffLines = 4000 // per side; larger texts are compared as a summary
	maxDiffShown = 300  // lines of diff output
)

// lineDiff shows how new differs from old, line by line: "- " for removed
// lines, "+ " for added ones and "  " for unchanged context.
func lineDiff(old, new string) string {
	a, b := strings.Split(old, "\n"), strings.Split(new, "\n")
	if old == "" {
		a = nil
	}
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		return fmt.Sprintf("(too long to compare line by line: %d lines before, %d after)", len(a), len(b))
	}
	ops := diffOps(a, b)
	var out []string
	for i, op := range ops {
		if op.kind == ' ' && !nearChange(ops, i) {
			if len(out) > 0 && out[len(out)-1] != "  ..." {
				out = append(out, "  ...")
			}
			continue
		}
		out = append(out, string(op.kind)+" "+op.line)
	}
	if len(out) > 0 && out[len(out)-1] == "  ..." {
		out = out[:len(out)-1]
	}
	if len(out) > maxDiffShown {
		out = append(out[:maxDiffShown], fmt.Sprintf("  ... and %d more lines", len(out)-maxDiffShown))
	}
	return strings.Join(out, "\n")
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	line string
}

func nearChange(ops []diffOp, i int) bool {
	for j := max(0, i-diffContext); j <= min(len(ops)-1, i+diffContext); j++ {
		if ops[j].kind != ' ' {
			return true
		}
	}
	return false
}

// diffOps computes a shortest edit script with a longest common
// subsequence table, which is fine for page-sized texts.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
