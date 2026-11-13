package main

import "testing"

// share add and rm rewrite config.toml by hand; an editor that touches a byte
// outside the array loses the user's comments or ordering.
func TestTomlArrayEdits(t *testing.T) {
	const head = "# mine\n[shares.other]\nitems = [\"x\"]\n\n[shares.s]  # the one\nsource = \"~/src\"\n"
	const tail = "into = \"~/dst\"   # after\n\n[tools.claude]\nitems = 1\n"
	cases := []struct {
		name, array, add, rm string
		rmIndex              int
	}{
		{"one line", `items = ["a", "b"]` + "\n",
			`items = ["a", "b", "c"]` + "\n", `items = ["b"]` + "\n", 0},
		{"one line, remove last", `items = ['a', "b"]` + "\n",
			`items = ['a', "b", "c"]` + "\n", `items = ['a']` + "\n", 1},
		{"one per line", "items = [\n  \"a\",  # first\n  \"b\"\n]\n",
			"items = [\n  \"a\",  # first\n  \"b\",\n  \"c\",\n]\n", "items = [\n  \"b\"\n]\n", 0},
		{"one per line, trailing comma", "items = [\n    \"a\",\n    \"b\", # second\n]\n",
			"items = [\n    \"a\",\n    \"b\", # second\n    \"c\",\n]\n", "items = [\n    \"a\",\n]\n", 1},
		{"last shares the closing line", "items = [\"a\",\n  \"b\"]\n",
			"items = [\"a\",\n  \"b\",\n  \"c\"]\n", "items = [\n  \"b\"]\n", 0},
		{"first string opens the array", "items = [\"a\"\n]\n",
			"items = [\"a\",\n\"c\",\n]\n", "items = [\n]\n", 0},
		{"two strings on the last line", "items = [\n  \"a\", \"b\",\n]\n",
			"items = [\n  \"a\", \"b\",\n  \"c\",\n]\n", "items = [\n  \"b\",\n]\n", 0},
	}
	for _, c := range cases {
		src := []byte(head + c.array + tail)
		a, err := findShareArray(src, "s", "items")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got, want := string(a.appended("c")), head+c.add+tail; got != want {
			t.Errorf("%s: add\n got %q\nwant %q", c.name, got, want)
		}
		if got, want := string(a.removed(c.rmIndex)), head+c.rm+tail; got != want {
			t.Errorf("%s: rm\n got %q\nwant %q", c.name, got, want)
		}
	}
}

func TestTomlArrayRefusesOtherLayouts(t *testing.T) {
	for _, src := range []string{
		"[shares]\ns = { source = \"x\", items = [\"a\"] }\n",
		"[shares.s]\nitems = \"*\"\n",
		"[shares.s]\nitems = [1, 2]\n",
	} {
		if _, err := findShareArray([]byte(src), "s", "items"); err == nil {
			t.Errorf("found an array in %q", src)
		}
	}
}
