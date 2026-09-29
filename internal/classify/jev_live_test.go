//go:build jev

package classify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestJevInjectionFixtures(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	if key == "" {
		// Local convenience: accept a leftover key file when the environment has none.
		if cfgDir, err := os.UserConfigDir(); err == nil {
			if data, err := os.ReadFile(filepath.Join(cfgDir, "relevo", "typesafe.key")); err == nil {
				key = strings.TrimSpace(string(data))
			}
		}
	}
	if key == "" {
		t.Skip("skipping live test: no TYPESAFE_API_KEY or ~/.config/relevo/typesafe.key found")
	}

	client := NewClient(key, "jev-latest")

	runFixtures := func(t *testing.T, dir string, isPositive bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		sort.Strings(files)

		for _, fpath := range files {
			fname := filepath.Base(fpath)
			t.Run(fname, func(t *testing.T) {
				data, err := os.ReadFile(fpath)
				if err != nil {
					t.Fatal(err)
				}
				paras := Split(data)
				kept, _ := Trim(paras)
				if len(kept) == 0 {
					t.Fatal("no paragraphs found in fixture")
				}

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				ans, err := client.Judge(ctx, Request{
					Source:     "report",
					Harness:    "claude",
					Paragraphs: kept,
				})
				if err != nil {
					t.Fatalf("Judge failed: %v", err)
				}

				maxP := 0.0
				fmt.Printf("\n--- Fixture: %s (%s) ---\n", fname, map[bool]string{true: "positive", false: "negative"}[isPositive])
				fmt.Printf("%-25s %-5s %-8s %-6s %s\n", "File", "Index", "Kind", "P", "Preview")
				for i, p := range kept {
					prob := 0.0
					if i < len(ans.Probabilities) {
						prob = ans.Probabilities[i]
					}
					if prob > maxP {
						maxP = prob
					}
					preview := strings.ReplaceAll(p.Text, "\n", " ")
					if len(preview) > 60 {
						preview = preview[:60] + "..."
					}
					fmt.Printf("%-25s %-5d %-8s %-6.3f %s\n", fname, p.Index, p.Kind, prob, preview)
				}

				if isPositive {
					if maxP < 0.7 {
						t.Errorf("%s: max(p) = %.3f < 0.7", fname, maxP)
					}
				} else {
					if maxP >= 0.7 {
						t.Errorf("%s: max(p) = %.3f >= 0.7", fname, maxP)
					}
				}
			})
		}
	}

	t.Run("positive", func(t *testing.T) {
		runFixtures(t, filepath.Join("testdata", "injection", "positive"), true)
	})

	t.Run("negative", func(t *testing.T) {
		runFixtures(t, filepath.Join("testdata", "injection", "negative"), false)
	})
}
