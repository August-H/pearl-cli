package verification

import "testing"

func TestRecognizesExecutedTestCommands(t *testing.T) {
	for _, test := range []struct {
		command string
		want    bool
	}{
		{"PYTHONDONTWRITEBYTECODE=1 python3 -m unittest -v", true},
		{"cd app && go test ./...", true},
		{"npm run test -- --run", true},
		{"/usr/bin/python3.12 -m pytest", true},
		{"pytest", true},
		{"echo pytest", false},
		{"pytest-fake", false},
		{"python3 -c 'print(\"pytest\")'", false},
		{"cat <<'EOF'\npython3 -m unittest\nEOF", false},
		{"apply_patch <<'PATCH'\n+go test ./...\nPATCH\npython3 -m unittest -v", false},
		{"pytest || true", false},
		{"pytest; echo done", false},
		{"pytest | tee results", false},
		{"echo 'example; pytest'", false},
		{"cd '/tmp/test app' && python3 -m unittest -v", true},
		{"python3 -m unittest && go test ./...", true},
	} {
		if got := Shell(test.command); got != test.want {
			t.Errorf("Shell(%q)=%v", test.command, got)
		}
	}
}
