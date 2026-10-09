package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	if !VerifyPassword("secret123", hash) {
		t.Error("the correct password was rejected")
	}
	if VerifyPassword("secret124", hash) {
		t.Error("a wrong password was accepted")
	}
	if VerifyPassword("", hash) {
		t.Error("an empty password was accepted")
	}
}

func TestHashIsSalted(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}

	// Identical hashes would mean the database leaks which accounts share a
	// password, so every hash must be unique.
	if string(first) == string(second) {
		t.Error("two hashes of the same password are identical; salt is not random")
	}
	if !VerifyPassword("same-password", second) {
		t.Error("the second hash does not verify")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	valid, err := HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}

	malformed := []string{
		"", "not-a-hash", "$argon2id$v=19$m=65536,t=3,p=2$onlysalt",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$a2V5", // wrong algorithm
		"$argon2id$v=19$m=65536$abc$def",            // too few parameters
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdA$a2V5",    // zero cost
	}
	for _, h := range malformed {
		if VerifyPassword("secret123", []byte(h)) {
			t.Errorf("malformed hash %q was accepted", h)
		}
	}

	// Sanity check that the well-formed hash in the table still verifies, so a
	// broken parser cannot make the whole test pass by rejecting everything.
	if !VerifyPassword("secret123", valid) {
		t.Error("control hash failed to verify")
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Error("expected a short password to be rejected")
	}
	if err := ValidatePassword("12345678"); err != nil {
		t.Errorf("expected an eight character password to be accepted, got %v", err)
	}
}

func TestGenerateTokenIsHashedNotStored(t *testing.T) {
	plaintext, hash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" || len(hash) == 0 {
		t.Fatal("expected both a token and a hash")
	}
	if got := HashToken(plaintext); string(got) != string(hash) {
		t.Error("HashToken does not reproduce the stored hash")
	}
	// Two tokens must never collide.
	secondPlain, secondHash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if secondPlain == plaintext || string(secondHash) == string(hash) {
		t.Error("generated tokens are not unique")
	}
}

func TestHasScope(t *testing.T) {
	tests := []struct {
		granted []string
		want    string
		expect  bool
	}{
		{granted: []string{"api"}, want: "write_repository", expect: true},
		{granted: []string{"api"}, want: "read_repository", expect: true},
		{granted: []string{"read_api"}, want: "read_user", expect: true},
		{granted: []string{"read_api"}, want: "write_repository", expect: false},
		{granted: []string{"read_repository"}, want: "read_repository", expect: true},
		{granted: []string{"read_repository"}, want: "write_repository", expect: false},
		{granted: nil, want: "api", expect: false},
	}

	for _, tc := range tests {
		if got := HasScope(tc.granted, tc.want); got != tc.expect {
			t.Errorf("HasScope(%v, %q) = %v, want %v", tc.granted, tc.want, got, tc.expect)
		}
	}
}

func TestValidScopesFiltersUnknownScopes(t *testing.T) {
	got := ValidScopes([]string{"read_user", "admin_everything", "read_user", "bogus"})
	if len(got) != 1 || got[0] != "read_user" {
		t.Errorf("got %v, want only read_user", got)
	}
}
