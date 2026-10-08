package resource

import "testing"

func TestCheckRefusesAnEmptyRequiredField(t *testing.T) {
	database, ok := ByKey("db")
	if !ok {
		t.Fatal("there is no db kind")
	}

	problems := database.Check(Parts{"host": "db.example.com", "port": "5432"})
	if len(problems) != 2 {
		t.Fatalf("wanted the database and the user named, got %v", problems)
	}
}

func TestCheckLetsAPasswordBeEmpty(t *testing.T) {
	database, _ := ByKey("db")

	problems := database.Check(Parts{
		"host": "db.example.com", "database_name": "deploy", "username": "deploy",
	})
	if len(problems) != 0 {
		t.Fatalf("a database that trusts its network has no password, and refusing to write that "+
			"down pushes the administrator into inventing one: %v", problems)
	}
}

func TestCheckRefusesAPortThatIsNotOne(t *testing.T) {
	database, _ := ByKey("db")

	for _, port := range []string{"abc", "0", "70000", "54 32", "-1"} {
		problems := database.Check(Parts{
			"host": "h", "port": port, "database_name": "d", "username": "u",
		})
		if len(problems) != 1 {
			t.Fatalf("port %q should have been refused, got %v", port, problems)
		}
	}
	if problems := database.Check(Parts{
		"host": "h", "port": "5432", "database_name": "d", "username": "u",
	}); len(problems) != 0 {
		t.Fatalf("5432 is a port: %v", problems)
	}
}

func TestFilledPutsTheDefaultInRatherThanLeavingTheModuleToGuess(t *testing.T) {
	database, _ := ByKey("db")

	filled := database.Filled(Parts{"host": "h", "database_name": "d", "username": "u"})
	if filled["port"] != "5432" {
		t.Fatalf("an empty port left for the module to guess at becomes a connect error that "+
			"names neither the field nor the store, wanted 5432, got %q", filled["port"])
	}
	// And what was typed is not overwritten by the default.
	filled = database.Filled(Parts{"host": "h", "port": "3306"})
	if filled["port"] != "3306" {
		t.Fatalf("the default overwrote a port somebody typed: %q", filled["port"])
	}
}

func TestSplitKeepsThePasswordOutOfTheColumns(t *testing.T) {
	database, _ := ByKey("db")
	plain, secret := database.Split(Parts{
		"host": "h", "port": "5432", "database_name": "d",
		"username": "u", "password": "hunter2",
	})

	if secret["password"] != "hunter2" {
		t.Fatalf("the password did not reach the envelope: %v", secret)
	}
	if _, there := plain["password"]; there {
		t.Fatalf("the password is in the columns, where a page would draw a hostname by "+
			"unsealing one: %v", plain)
	}
	// The facts are what a page shows without decrypting anything.
	if plain["host"] != "h" || plain["database_name"] != "d" || plain["username"] != "u" {
		t.Fatalf("the facts did not reach the columns: %v", plain)
	}
}

func TestSplitRecordsNoSecretRatherThanAnEmptyOne(t *testing.T) {
	database, _ := ByKey("db")
	_, secret := database.Split(Parts{
		"host": "h", "database_name": "d", "username": "u", "password": "",
	})

	if len(secret) != 0 {
		t.Fatalf("a password field left empty was recorded as a password of nothing: %v", secret)
	}
}

func TestSplitKeepsTheObjectStoreSecretOutOfTheColumns(t *testing.T) {
	store, _ := ByKey("s3")
	plain, secret := store.Split(Parts{
		"endpoint": "https://minio:9000", "region": "eu", "bucket": "images",
		"access_key": "AKIA", "secret_key": "s3cr3t",
	})

	if secret["secret_key"] != "s3cr3t" {
		t.Fatalf("the secret key did not reach the envelope: %v", secret)
	}
	if _, there := plain["secret_key"]; there {
		t.Fatalf("the secret key is in the columns: %v", plain)
	}
	// The access key is the readable half, kept so a page can say which key is in use.
	if plain["access_key"] != "AKIA" {
		t.Fatalf("the access key should stay readable: %v", plain)
	}
}

func TestPayloadCarriesTheSecretsAndDropsTheEmpties(t *testing.T) {
	database, _ := ByKey("db")
	payload := database.Payload(Parts{
		"host": "h", "database_name": "d", "username": "u", "password": "",
	})

	if payload["host"] != "h" || payload["database_name"] != "d" || payload["username"] != "u" {
		t.Fatalf("the payload is missing what the module needs: %v", payload)
	}
	if _, there := payload["password"]; there {
		t.Fatalf("an empty part was sent as an empty value, and a module cannot tell that from "+
			"a password that happens to be empty: %v", payload)
	}
}

func TestPayloadCarriesNothingFromAnotherKind(t *testing.T) {
	database, _ := ByKey("db")
	payload := database.Payload(Parts{
		"host": "h", "database_name": "d", "username": "u",
		"bucket": "images", "secret_key": "s3cr3t",
	})

	for _, key := range []string{"bucket", "secret_key"} {
		if _, there := payload[key]; there {
			t.Fatalf("a db resource handed its module an s3 part: %v", payload)
		}
	}
}

func TestIdentityIgnoresTheSecrets(t *testing.T) {
	database, _ := ByKey("db")
	first := database.Identity(Parts{"host": "h", "database_name": "d", "username": "u", "password": "one"})
	second := database.Identity(Parts{"host": "h", "database_name": "d", "username": "u", "password": "two"})

	if first["password"] != "" || second["password"] != "" {
		t.Fatal("the password is part of the identity, so a stale one cannot be written down")
	}
	if len(first) != len(second) || first["host"] != second["host"] {
		t.Fatalf("two records of the same place reached by the same user are not the same: %v vs %v",
			first, second)
	}
}

func TestEveryKindHasANameASoftwareAndNoDuplicateFields(t *testing.T) {
	for _, kind := range Kinds() {
		if kind.Key == "" || kind.Label == "" {
			t.Fatalf("a kind nobody can pick: %+v", kind)
		}
		seen := map[string]bool{}
		for _, field := range kind.Fields {
			if field.Key == "" || field.Label == "" {
				t.Fatalf("%s has a field nobody can fill in: %+v", kind.Key, field)
			}
			if seen[field.Key] {
				t.Fatalf("%s asks for %s twice", kind.Key, field.Key)
			}
			seen[field.Key] = true
		}
		if len(kind.IdentityKeys()) == 0 {
			t.Fatalf("%s has nothing but secrets, so two of its records can never be told apart",
				kind.Key)
		}
	}
}
