// Package resource describes the kinds of resource this instance knows about, and what parts
// each one is made of.
//
// One place for the parts, because a resource is written down in four of them — the form an
// administrator fills in, the check before the record is saved, the columns it is stored in, and
// the payload a module is handed — and four lists of the same thing drift apart. A database that
// is host, port, database, user and password in the form and host, port and password in the
// table is a database whose user was asked for and then silently dropped, and nothing about that
// looks like a bug from where it is typed.
//
// The core deliberately does not turn these parts into a connection string. What a driver
// accepts is the driver's business and not the core's: a module that wants `postgres://…` and
// one that wants a keyword/value DSN both start from the same six facts and disagree about
// everything else. So the core hands over the parts and the module assembles them, and a
// resource of a kind this package has never heard of can still be written down by name.
package resource

import (
	"fmt"
	"strconv"
	"strings"
)

// Field is one part of a resource.
//
// Key is at once the column, the key in the payload, and the name of the form field. That is on
// purpose: the three are the same word, and giving them three spellings would be a way of
// inventing three things to keep in step.
type Field struct {
	// Key is the name of the part.
	Key string

	// Label is what a person reads above the input.
	Label string

	// Hint is the sentence under it. It says what to put there, not what the field is for —
	// the label already does that, and a hint that repeats it is a hint nobody reads.
	Hint string

	// Secret parts are sealed and are never rendered into a page. They are listed here all the
	// same, because the form needs to ask for them and the module needs to be handed them.
	Secret bool

	// Required parts are refused when empty. Optional is a deliberate word: a database that
	// trusts its network has no password, and refusing to write that down would push the
	// administrator into inventing one.
	Required bool

	// Port is digits only, and is the only part with a range.
	Port bool

	// Default is filled in for an empty optional field, and is what the form pre-fills.
	Default string
}

// Kind is one sort of resource: what it is called, and what it is made of.
type Kind struct {
	// Key is the class a requirement is written against: `db`, `s3`.
	Key string

	// Label is what a person reads in the form.
	Label string

	// Note says what this instance does with this kind, as opposed to what it is. It belongs
	// on the form above the fields, because "an object store is only ever described here" is
	// the answer to the question an administrator arrives with.
	Note string

	// Software is what may go in the software field, offered as a choice rather than typed.
	// Empty means this kind is not a piece of software at all.
	Software []string

	// Fields are its parts, in the order a form should ask for them.
	Fields []Field
}

// The kinds. A database first, because it is the one this instance may also make for itself;
// an object store after it, because nothing here ever creates one.
var kinds = []Kind{
	{
		Key:   "db",
		Label: "Database",
		Note: "This instance makes databases of its own when a module asks for one and has none. " +
			"Describing one here is for a database somewhere else — a host you already have, or one " +
			"you would rather this instance could not reach.",
		Software: []string{"postgresql", "mysql"},
		Fields: []Field{
			{
				Key: "host", Label: "Host", Required: true,
				Hint: "The machine the database answers on, as this module will see it. Not always " +
					"the address you would type in a browser: from inside a container it is often " +
					"another name entirely.",
			},
			{
				Key: "port", Label: "Port", Port: true, Default: "5432",
				Hint: "5432 for PostgreSQL, 3306 for MySQL.",
			},
			{
				Key: "database_name", Label: "Database", Required: true,
				Hint: "The database itself, which is not the same thing as the user: one user owns " +
					"several databases, and a module is usually given one of them rather than all.",
			},
			{
				Key: "username", Label: "User", Required: true,
				Hint: "The role this module connects as. Give it one of its own rather than the " +
					"superuser — the module has no use for the difference and cannot undo it later.",
			},
			{
				Key: "password", Label: "Password", Secret: true,
				Hint: "Leave empty if the database trusts the network. Written once and sealed; " +
					"there is no field that reads it back.",
			},
		},
	},
	{
		Key:   "s3",
		Label: "Object store",
		Note: "Object stores are only ever described here. This instance creates none, and nothing " +
			"in it negotiates with one — a module is handed what you write down and works out the " +
			"rest.",
		Fields: []Field{
			{
				Key: "endpoint", Label: "Address", Required: true,
				Hint: "The whole URL, as the module will reach it: https://minio.example.com:9000",
			},
			{
				Key: "region", Label: "Region",
				Hint: "Optional. Some stores insist on one and some reject it; where it is required " +
					"and left empty, the error comes from the store and names the field itself.",
			},
			{
				Key: "bucket", Label: "Bucket", Required: true,
				Hint: "The bucket this module may use, rather than the account: a key that can reach " +
					"every bucket is a key that has to be trusted with all of them.",
			},
			{
				Key: "access_key", Label: "Access key", Required: true,
				Hint: "The public half of the pair, kept readable so that a page can say which key " +
					"is in use.",
			},
			{
				Key: "secret_key", Label: "Secret key", Secret: true, Required: true,
				Hint: "Written once and sealed. There is no field that reads it back.",
			},
		},
	},
}

// Parts is what a resource is made of, by field key.
//
// Secrets live here too while they are being written down, and are split out on the way to
// storage — see Kind.Split.
type Parts map[string]string

// Kinds is every kind this instance can describe, in the order a form should offer them.
func Kinds() []Kind { return kinds }

// ByKey finds one kind by its class.
func ByKey(key string) (Kind, bool) {
	for _, kind := range kinds {
		if kind.Key == key {
			return kind, true
		}
	}
	return Kind{}, false
}

// Field finds one part of this kind by its name.
func (k Kind) Field(key string) (Field, bool) {
	for _, field := range k.Fields {
		if field.Key == key {
			return field, true
		}
	}
	return Field{}, false
}

// Filled returns the parts with this kind's defaults put in where a field was left empty.
//
// Defaults are filled in rather than left to the module: an empty port is a port the module has
// to guess at, and a module that guesses 5432 at a MySQL database fails at connect time with an
// error that names neither the field nor the store.
func (k Kind) Filled(parts Parts) Parts {
	out := make(Parts, len(k.Fields))
	for _, field := range k.Fields {
		value := strings.TrimSpace(parts[field.Key])
		if value == "" {
			value = field.Default
		}
		out[field.Key] = value
	}
	return out
}

// Check returns everything that is wrong with a set of parts, in the words a form should show.
//
// All of it at once rather than the first thing: a form that refuses one field at a time makes
// somebody save, wait to be told about the next, and fix it.
func (k Kind) Check(parts Parts) []string {
	var problems []string
	for _, field := range k.Fields {
		value := strings.TrimSpace(parts[field.Key])
		if field.Required && value == "" {
			problems = append(problems, fmt.Sprintf("%s is required", strings.ToLower(field.Label)))
			continue
		}
		if value == "" {
			continue
		}
		if field.Port {
			number, err := strconv.Atoi(value)
			if err != nil || number < 1 || number > 65535 {
				problems = append(problems, fmt.Sprintf(
					"%s must be a number between 1 and 65535, and %q is neither",
					strings.ToLower(field.Label), value))
			}
		}
	}
	return problems
}

// Split separates the parts that go into columns from the parts that go into the sealed envelope.
//
// This is the line the package exists to keep straight. A host, a database, a user and a bucket
// are facts about where a thing is, and a page can show them to somebody deciding what to do
// next. A password is not a fact and showing it changes who can read the page. Putting both in
// one sealed blob means the page has to unseal a password to draw a hostname, and a page that
// unseals anything eventually prints one.
func (k Kind) Split(parts Parts) (plain, secret Parts) {
	plain, secret = Parts{}, Parts{}
	for _, field := range k.Fields {
		value := parts[field.Key]
		if field.Secret {
			// Only a secret that was actually given. A password field left empty is not a
			// password of "" — it is no password — and recording it as one means the module
			// is handed a credential it must decide is empty, while the page cannot say
			// "none" because there is a key with nothing behind it.
			if value != "" {
				secret[field.Key] = value
			}
			continue
		}
		plain[field.Key] = value
	}
	return plain, secret
}

// Plain returns only the parts that are safe to show and to store in columns.
func (k Kind) Plain(parts Parts) Parts {
	plain, _ := k.Split(parts)
	return plain
}

// Payload is what a module is handed: every part this kind has, secrets included, and nothing
// belonging to another kind.
//
// Keyed by the same words the columns are, so a module reads `payload["database_name"]` and
// knows what it is. Empty parts are dropped rather than sent as empty strings: a module that
// cannot tell "no password" from "a password that happens to be empty" will invent one.
func (k Kind) Payload(parts Parts) map[string]string {
	out := make(map[string]string, len(k.Fields))
	for _, field := range k.Fields {
		if value := parts[field.Key]; value != "" {
			out[field.Key] = value
		}
	}
	return out
}

// Identity says which thing a set of parts refers to, regardless of what secret reaches it.
//
// What makes two records the same resource asked for twice: the same place, reached by the same
// user, into the same database. Not the password — a record whose password has gone stale is
// still a record of the same database, and refusing to add it would leave the administrator with
// a database they cannot describe rather than with a second description of one they have.
func (k Kind) Identity(parts Parts) map[string]string {
	identity := make(map[string]string, len(k.Fields))
	for _, field := range k.Fields {
		if field.Secret {
			continue
		}
		identity[field.Key] = parts[field.Key]
	}
	return identity
}

// IdentityKeys lists the parts that make up an identity, in the kind's own order. Used to build
// the index and the duplicate check, so that the two cannot be written differently.
func (k Kind) IdentityKeys() []string {
	keys := make([]string, 0, len(k.Fields))
	for _, field := range k.Fields {
		if !field.Secret {
			keys = append(keys, field.Key)
		}
	}
	return keys
}
