Not named bad-*: that glob maps a directory name to a rule id, and this fixture
violates html.source-pinned, whose bad-* fixture already exists. The shape here
is the one that used to pass — a whole second <section id="sources">, appended
after the real one so nothing in the real list changes. Every rule that resolves
a citation reads the first such section, so the entry below was cited by nothing
and read by nothing, while sitting under a Sources heading a reader can see.
