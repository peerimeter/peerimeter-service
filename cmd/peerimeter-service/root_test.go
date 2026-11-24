package main

import "testing"

func TestRootCommandHasExplicitServeAndSchemaMigrationCommands(t *testing.T) {
	root := newRootCommand()
	for _, name := range []string{"serve", "migrate"} {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Errorf("root command does not find %q: %v", name, err)
		}
	}
	if root.Run != nil || root.RunE != nil {
		t.Fatal("root command should display help instead of starting the server")
	}
}
