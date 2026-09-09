Not named bad-*: that glob maps a directory name to a rule id, and this fixture
violates html.source-pinned, whose bad-* fixture already exists. The shape here
is the one that used to pass — an <li> copied to add a citation, keeping the id
it was copied from, so the entry was dropped before any rule read its URL.
