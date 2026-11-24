package wire

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guestskills"
)

// TestSkillsInEveryHarness: every harness gets every LydiaBox skill in
// its skill directory, in the gateway mode and in guest-native mode; a
// second apply changes nothing; guest-native mode reads and writes no
// credential file (guardedHome).
func TestSkillsInEveryHarness(t *testing.T) {
	if len(SkillDirs) != len(All()) {
		t.Fatalf("%d skill entries for %d harnesses", len(SkillDirs), len(All()))
	}
	for _, native := range []bool{false, true} {
		for _, w := range All() {
			home := t.TempDir()
			var h Home = DirHome(home)
			if native {
				h = guardedHome{t: t, home: DirHome(home)}
			}
			b := Binding{Native: native}
			if err := w.Apply(h, b); err != nil {
				t.Fatalf("%s native=%v: %v", w.Harness(), native, err)
			}
			dirs := SkillDirs[w.Harness()]
			if len(dirs) == 0 {
				t.Fatalf("%s: no skill directory", w.Harness())
			}
			mtimes := map[string]time.Time{}
			for _, dir := range dirs {
				for _, s := range guestskills.All() {
					p := filepath.Join(home, dir, s.Name, guestskills.FileName)
					got, err := os.ReadFile(p)
					if err != nil || string(got) != string(s.Content) {
						t.Errorf("%s native=%v: %s: %v", w.Harness(), native, p, err)
						continue
					}
					fi, _ := os.Stat(p)
					mtimes[p] = fi.ModTime()
				}
			}
			// Idempotent: the second apply writes no skill file again.
			past := time.Now().Add(-time.Hour)
			for p := range mtimes {
				if err := os.Chtimes(p, past, past); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Apply(h, b); err != nil {
				t.Fatalf("%s: second apply: %v", w.Harness(), err)
			}
			for p := range mtimes {
				if fi, _ := os.Stat(p); !fi.ModTime().Equal(past) {
					t.Errorf("%s: %s was written again", w.Harness(), p)
				}
			}
		}
	}
}

// TestSkillsKeepUserSkills: a skill of the user (any other directory) and
// a LydiaBox skill file that the user replaced stay as they are; an old
// managed file is replaced.
func TestSkillsKeepUserSkills(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	write := func(rel, data string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	skills := guestskills.All()
	user := write("my-skill/SKILL.md", "---\nname: my-skill\ndescription: Mine.\n---\n")
	extra := write("my-skill/notes.txt", "x")
	mine := write(skills[0].Name+"/SKILL.md", "---\nname: "+skills[0].Name+"\ndescription: My own version.\n---\n")
	old := write(skills[1].Name+"/SKILL.md", "old text\n"+guestskills.Marker+"\n")
	userFile := write(skills[1].Name+"/my-file.md", "keep")

	if err := WriteSkills(DirHome(home), ".claude/skills"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		user:     "---\nname: my-skill\ndescription: Mine.\n---\n",
		extra:    "x",
		mine:     "---\nname: " + skills[0].Name + "\ndescription: My own version.\n---\n",
		old:      string(skills[1].Content),
		userFile: "keep",
	} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", p, got, err, want)
		}
	}
	var n int
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if n != 5 {
		t.Errorf("%d files below %s, want 5", n, root)
	}
}
