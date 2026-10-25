package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memorySecrets struct {
	values map[string]string
	fail   bool
	gets   int
}

func (s *memorySecrets) Get(id string) (string, error) {
	s.gets++
	if s.fail {
		return "", errors.New("keychain locked")
	}
	value, ok := s.values[id]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}
func (s *memorySecrets) Set(id, password string) error {
	if s.fail {
		return errors.New("keychain locked")
	}
	s.values[id] = password
	return nil
}
func (s *memorySecrets) Delete(id string) error {
	if s.fail {
		return errors.New("keychain locked")
	}
	delete(s.values, id)
	return nil
}

func testProfile() Profile {
	p := New()
	p.Name = "Local"
	p.Database = "postgres"
	p.User = "test"
	return p
}

func TestProfileLifecycleAndCredentials(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "settings", "connections.json")}
	secrets := &memorySecrets{values: make(map[string]string)}
	m := Manager{Profiles: store, Secrets: secrets}
	p := testProfile()
	profiles, warning, err := m.Save(p, "secret-that-must-not-be-in-json")
	if err != nil || warning != "" {
		t.Fatalf("save: %v %s", err, warning)
	}
	p = profiles[0]
	if p.PasswordMode != Keychain {
		t.Fatal("password should be in keychain")
	}
	if password, _, prompt := m.Password(p); prompt || password != "secret-that-must-not-be-in-json" {
		t.Fatal("saved password not resolved")
	}
	data, err := os.ReadFile(store.Path)
	if err != nil || strings.Contains(string(data), "secret-that-must-not-be-in-json") {
		t.Fatal("configuration contains secret or is unreadable")
	}
	info, _ := os.Stat(store.Path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions %v", info.Mode())
	}
	p.Name = "Renamed"
	profiles, _, err = m.Save(p, "")
	if err != nil || len(profiles) != 1 || profiles[0].Name != "Renamed" || profiles[0].ID != p.ID {
		t.Fatal("edit did not preserve identity", err)
	}
	p = profiles[0]
	if _, ok := secrets.values[p.ID]; ok {
		t.Fatal("clearing password did not remove secret")
	}
	gets := secrets.gets
	for i := 0; i < 3; i++ {
		if password, _, prompt := m.Password(p); !prompt || password != "" {
			t.Fatal("must prompt on every connection")
		}
	}
	if secrets.gets != gets {
		t.Fatal("prompt mode consulted keychain")
	}
	profiles, _, err = m.Delete(p.ID)
	if err != nil || len(profiles) != 0 {
		t.Fatal("delete failed", err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 0 {
		t.Fatal("delete did not persist", err)
	}
}

func TestKeychainFailureFallsBackToPrompt(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "connections.json")}
	secrets := &memorySecrets{values: map[string]string{}, fail: true}
	m := Manager{Profiles: store, Secrets: secrets}
	profiles, warning, err := m.Save(testProfile(), "password")
	if err != nil || warning == "" || profiles[0].PasswordMode != Prompt {
		t.Fatal("must explicitly fall back to prompting", err, warning)
	}
	p := profiles[0]
	p.PasswordMode = Keychain
	if _, explanation, prompt := m.Password(p); !prompt || explanation == "" {
		t.Fatal("keychain failures need an explained prompt")
	}
	secrets.fail = false
	if _, _, prompt := m.Password(p); !prompt {
		t.Fatal("missing secret should prompt")
	}
}

func TestInvalidConfigIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := &memorySecrets{values: map[string]string{}}
	m := Manager{Profiles: FileStore{Path: path}, Secrets: secrets}
	if _, _, err := m.Save(testProfile(), "password"); err == nil {
		t.Fatal("must reject invalid existing configuration")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "broken JSON" || len(secrets.values) != 0 {
		t.Fatal("invalid configuration was mutated")
	}
}

type failingRepository struct{ profiles []Profile }

func (f failingRepository) Load() ([]Profile, error) {
	return append([]Profile(nil), f.profiles...), nil
}
func (f failingRepository) Save([]Profile) error { return errors.New("disk full") }

func TestSecretRollbackOnConfigFailure(t *testing.T) {
	p := testProfile()
	p.PasswordMode = Keychain
	secrets := &memorySecrets{values: map[string]string{p.ID: "original"}}
	m := Manager{Profiles: failingRepository{[]Profile{p}}, Secrets: secrets}
	if _, _, err := m.Save(p, "replacement"); err == nil {
		t.Fatal("expected disk error")
	}
	if secrets.values[p.ID] != "original" {
		t.Fatal("original secret not restored")
	}
}
