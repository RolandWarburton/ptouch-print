package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The board firmware is C, so it has its own copies of the result codes and the
// media table. These tests keep them alike.

func readFirmware(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../firmware/" + path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wireCodes is every Code constant in wire.go, by its value.
func wireCodes(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "wire.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if ok && len(v.Values) == 1 && strings.HasPrefix(v.Names[0].Name, "Code") {
			if lit, ok := v.Values[0].(*ast.BasicLit); ok {
				s, _ := strconv.Unquote(lit.Value)
				codes[s] = v.Names[0].Name
			}
		}
		return true
	})
	return codes
}

func TestFirmwareCodes(t *testing.T) {
	codes := wireCodes(t)
	defs := regexp.MustCompile(`(?m)^#define CODE_([A-Z_]+) "([a-z_]+)"`).
		FindAllStringSubmatch(readFirmware(t, "components/board/include/codes.h"), -1)
	if len(codes) == 0 || len(defs) == 0 {
		t.Fatalf("found %d codes in wire.go and %d in codes.h", len(codes), len(defs))
	}
	for _, d := range defs {
		if codes[d[2]] == "" {
			t.Errorf("CODE_%s is %q, which isn't one of wire's codes", d[1], d[2])
		}
		if strings.ToLower(d[1]) != d[2] {
			t.Errorf("CODE_%s is %q", d[1], d[2])
		}
	}
}

func TestFirmwareMedia(t *testing.T) {
	rows := regexp.MustCompile(`const media_t MEDIA_\w+ = \{"([^"]+)", "([^"]+)", (\d+), (\d+), (\d+)`).
		FindAllStringSubmatch(readFirmware(t, "components/label/label.c"), -1)
	firmware := map[string]Media{}
	for _, r := range rows {
		n := func(s string) int { i, _ := strconv.Atoi(s); return i }
		firmware[r[1]] = Media{ID: r[1], Name: r[2], Width: n(r[3]), Length: n(r[4]), Pitch: n(r[5])}
	}
	labels := 0
	for _, m := range media {
		if m.Length == 0 {
			continue // tape
		}
		labels++
		got := firmware[m.ID]
		got.Family = m.Family
		if got != m {
			t.Errorf("%s: the firmware has %+v, want %+v", m.ID, got, m)
		}
	}
	if labels == 0 || len(firmware) != labels {
		t.Errorf("%d kinds of die-cut labels here, %d in the firmware", labels, len(firmware))
	}
}
