package registry

// SyncChange is one edit `sync` would make or made.
type SyncChange struct {
	Action string `json:"action"           yaml:"action"` // add | remove | fix-file
	Family string `json:"family"           yaml:"family"`
	ID     string `json:"id"               yaml:"id"`
	File   string `json:"file,omitempty"   yaml:"file,omitempty"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
	// Missing lists, on an add, the fields the family's index schema
	// requires that the record has no value for. Sync copies and never
	// invents, so these are left out of the entry and named here instead.
	Missing []string `json:"missing,omitempty" yaml:"missing,omitempty"`
}

// SyncBlocked is a removal `sync` declined to make because it could not
// prove the entry is a phantom. See the withheld-removal rules on Sync.
type SyncBlocked struct {
	Family string `json:"family"         yaml:"family"`
	ID     string `json:"id"             yaml:"id"`
	File   string `json:"file,omitempty" yaml:"file,omitempty"`
	Reason string `json:"reason"         yaml:"reason"`
}

// SyncResult reports what reconciling did (or would do under --check).
type SyncResult struct {
	Families []SyncFamilyResult `json:"families" yaml:"families"`
	Changes  []SyncChange       `json:"changes"  yaml:"changes"`
	// Blocked are removals withheld to avoid deleting an index entry that
	// is the last copy of an unreadable record's metadata. Non-empty means
	// the family has a `registry.record_unparseable` finding to fix first.
	Blocked []SyncBlocked `json:"blocked" yaml:"blocked"`
	Check   bool          `json:"check"   yaml:"check"`
}

// SyncFamilyResult is the per-family outcome of a reconcile.
type SyncFamilyResult struct {
	Family  string `json:"family"           yaml:"family"`
	Index   string `json:"index,omitempty"  yaml:"index,omitempty"`
	Skipped bool   `json:"skipped"          yaml:"skipped"`
	Reason  string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Written bool   `json:"written"          yaml:"written"`
	Entries int    `json:"entries"          yaml:"entries"`
}

// SyncOptions selects what to reconcile.
type SyncOptions struct {
	Only []string
	// Check makes this a dry run: report the diff, write nothing. This is
	// the semantic the parent `sync` command's --check flag has always
	// declared.
	Check       bool
	IgnoreExt   bool
	GeneratedAt string
}

// UpdateResult reports a field-delta application.
type UpdateResult struct {
	Index   string `json:"index"   yaml:"index"`
	Family  string `json:"family"  yaml:"family"`
	Entries int    `json:"entries" yaml:"entries"`
	Fields  int    `json:"fields"  yaml:"fields"`
}

// UpdateValue is one field delta: either a scalar rendered as text, or a
// list of scalars rendered as a YAML sequence.
type UpdateValue struct {
	// Scalar is the text form; meaningful only when IsList is false.
	Scalar string
	// Items are the list's elements, each already in text form.
	Items []string
	// IsList distinguishes an empty list from an empty scalar — `[]` and
	// `""` are different values, and a length check cannot tell them apart.
	IsList bool
}
