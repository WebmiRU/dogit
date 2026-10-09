-- What a group is for, in one line.
--
-- A group is now a row in the same list a project is, and a row in that list says what
-- it is and, if it has one, what it is for. Without this the two kinds of row would
-- be unequal for no reason anybody could name: everything else about a place to work —
-- a name, a path, a description — a group would have had all but the third.
--
-- Empty rather than not-null-with-a-default: a group nobody has described yet is a
-- group nobody has described, and the page says so instead of inventing a sentence.
ALTER TABLE groups
    ADD COLUMN description TEXT NOT NULL DEFAULT '';