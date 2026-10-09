package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/secrets"
)

// A module that declares a credential, and one that does not.
func credentialModule() *models.Integration {
	return &models.Integration{
		Capabilities: models.Manifest{
			Settings: []models.SettingSpec{
				{Key: "registry_token", Label: "Registry token", Type: "string", Secret: true},
				{Key: "concurrency", Label: "Jobs at once", Type: "int"},
			},
		},
	}
}

func TestSealSettingLeavesAnOrdinaryValueAlone(t *testing.T) {
	s := &Server{}
	plain := json.RawMessage(`4`)

	got, err := s.sealSetting(credentialModule(), "concurrency", plain)
	if err != nil {
		t.Fatalf("an ordinary setting must not need a key: %v", err)
	}
	if string(got) != `4` {
		t.Fatalf("an ordinary setting came back changed: %s", got)
	}
}

func TestSealSettingRefusesACredentialWithoutAKey(t *testing.T) {
	s := &Server{}

	_, err := s.sealSetting(credentialModule(), "registry_token", json.RawMessage(`"hunter2"`))
	if err == nil {
		t.Fatal("a credential was accepted on an instance with no key, which is how it ends up in the clear")
	}
	if !strings.Contains(err.Error(), "DOGIT_SECRET_KEY") {
		t.Fatalf("the refusal must say what to do about it, got: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("the refusal quoted the credential: %v", err)
	}
}

func TestSealSettingKeepsACredentialOffDisk(t *testing.T) {
	sealer, err := newTestSealer()
	if err != nil {
		t.Fatalf("a sealer for the test: %v", err)
	}
	s := &Server{sealer: sealer}

	got, err := s.sealSetting(credentialModule(), "registry_token", json.RawMessage(`"hunter2"`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(string(got), "hunter2") {
		t.Fatalf("the value to be stored carries the credential in the clear: %s", got)
	}

	// And it is the same value on the way back, which is the half that makes it usable.
	back, err := sealer.Open(got)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(back) != `"hunter2"` {
		t.Fatalf("the credential did not come back as it went in: %s", back)
	}
}

func TestSealSettingTwiceDoesNotLookTheSame(t *testing.T) {
	sealer, _ := newTestSealer()
	s := &Server{sealer: sealer}

	one, err := s.sealSetting(credentialModule(), "registry_token", json.RawMessage(`"same"`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	two, err := s.sealSetting(credentialModule(), "registry_token", json.RawMessage(`"same"`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if string(one) == string(two) {
		t.Fatal("two stores of the same credential look identical, so a reader can tell them apart")
	}
}

func TestASettingNobodyDeclaredIsNotACredential(t *testing.T) {
	sealer, _ := newTestSealer()
	s := &Server{sealer: sealer}

	// An unknown key must not be sealed on a guess. If it were, a module could not write a
	// setting it had not declared, and neither could this instance.
	got, err := s.sealSetting(credentialModule(), "something_new", json.RawMessage(`"value"`))
	if err != nil {
		t.Fatalf("an undeclared setting must pass through: %v", err)
	}
	if strings.Contains(string(got), "__sealed") {
		t.Fatalf("an undeclared setting was sealed: %s", got)
	}
}

// A sealer for the tests, from a key that is deliberately not a secret.
func newTestSealer() (*secrets.Sealer, error) {
	return secrets.New(strings.Repeat("ab", 32))
}
