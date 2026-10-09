-- A registry is an address an administrator wrote down.
--
-- Not a record of what the instance runs: the registry this instance runs is a module
-- (kind registry:docker, an integrations row), and it already has its own page. This
-- table is for the registries that live elsewhere — the ones somebody pushes to and pulls
-- from by hand, which until now had nowhere to be written down at all and so lived in
-- shell history and in the heads of the people who knew.
--
-- One table per kind rather than one table with a kind column, because the three kinds
-- do not agree on what a record is. A Docker registry is a host, optionally with a port,
-- reached with a login and a password, and is either the default one or it is not. A
-- Composer or an npm repository is a URL with a path under it and is reached with a token
-- that is not a password and has no TLS switch worth speaking of. Sharing one table means
-- every column of one kind is nullable for the other two, and a nullable column is a
-- question the code has to answer at every read.
--
-- Only administrators may read or write this table, and nothing in the core reads it
-- yet: a registry written down here is not yet one anything deploys through, and this
-- migration does not pretend otherwise. It is the address book, first.
CREATE TABLE IF NOT EXISTS docker_registries (
    -- The name an administrator calls this registry by. Optional, and for the human only:
    -- nothing resolves through it, so two records may share one. Empty means the list
    -- shows the address, which is the only name that registry actually has.
    name TEXT NOT NULL DEFAULT '',

    -- The address, with the port if it has one: "registry.example.com", or
    -- "192.168.1.103:8091", or "harbor.example.com/v2" for a registry served under a
    -- path. Stored as written, and compared without regard to case, because a host does
    -- not care how it was typed — two records differing only in "Registry.Example.com"
    -- and "registry.example.com" are one registry written down twice.
    url TEXT NOT NULL,

    -- A credential for a registry that asks for one, empty for one that does not (most
    -- of them do not). The password is stored as written and is never sent back to a
    -- browser: the form shows that one is set, not what it is, so a list of ten
    -- registries does not put ten passwords into a page's source.
    login TEXT NOT NULL DEFAULT '',
    password TEXT NOT NULL DEFAULT '',

    -- A private registry is very often behind a certificate nobody signed: a
    -- self-signed one, or a host name in a certificate that does not match the address
    -- it is reached by. Both are refused by any correct client, and a flag that says so
    -- is the difference between a record and a decision — with it, the refusal is
    -- deliberate; without it, the registry is simply unreachable and nobody knows why.
    insecure_tls BOOLEAN NOT NULL DEFAULT FALSE,

    -- Whether images may be pushed here. A mirror or a read-only replica is pulled from
    -- and never written to, and knowing that in advance is better than finding out from
    -- a push that was rejected.
    read_only BOOLEAN NOT NULL DEFAULT FALSE,

    -- The one registry pushes go to when nothing has said otherwise. At most one, by the
    -- index below rather than by convention: two records each marked the default is a
    -- state the table itself refuses to hold.
    is_default BOOLEAN NOT NULL DEFAULT FALSE,

    -- Free text for the administrator: who owns this registry, what it is for, when it
    -- may be removed. Nothing reads it.
    note TEXT NOT NULL DEFAULT '',

    -- Turned off rather than deleted: a registry that is switched off is one nobody
    -- should be using, without losing the address somebody took the trouble to write
    -- down.
    enabled BOOLEAN NOT NULL DEFAULT TRUE,

    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT docker_registries_url_not_blank CHECK (btrim(url) <> '')
);

-- One address, one record, and the comparison is made on the address rather than on the
-- text: an index over lower(btrim(url)) rather than a UNIQUE on the column, because a
-- constraint on the column would call "Registry.Example.com" and "registry.example.com"
-- two different registries, and then the list would hold both while asking about either
-- one found a single row. Which of the two it finds would depend on the case it was
-- written in.
CREATE UNIQUE INDEX IF NOT EXISTS docker_registries_url_idx
    ON docker_registries (lower(btrim(url)));

-- One default, enforced here so that a second "make this the default" is refused by the
-- database rather than by whichever code path happened to remember to clear the first.
-- FALSE rows are excluded, so any number of records may be non-default.
CREATE UNIQUE INDEX IF NOT EXISTS docker_registries_default_key
    ON docker_registries (is_default) WHERE is_default;

COMMENT ON TABLE docker_registries IS
    'Docker registries written down by an administrator: addresses, credentials and how each is reached. Read by nothing yet.';
COMMENT ON COLUMN docker_registries.url IS
    'Host with an optional port and optional path prefix, as typed. Compared without regard to case.';
COMMENT ON COLUMN docker_registries.password IS
    'Stored as written, never returned to a browser: the form shows whether one is set.';
COMMENT ON COLUMN docker_registries.insecure_tls IS
    'True to accept a certificate that does not verify, which is what a self-signed private registry serves.';
COMMENT ON COLUMN docker_registries.read_only IS
    'True when images may be pulled from this registry but not pushed to it.';
COMMENT ON COLUMN docker_registries.is_default IS
    'At most one record in this table may be the default. The database enforces it.';
