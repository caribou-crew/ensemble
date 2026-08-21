// deviations.go — a deviation is a recorded human decision to tolerate a
// specific difference between two apps' runs: "these two are expected to
// differ here, and here is why". Task 11 owns the ledger that loads,
// resolves and matches them. The TYPES live here because this package's
// own structs reference them, and a struct field whose type is undeclared
// does not compile.
package diff

// Deviation is one entry in the ledger file named by config.Deviations.
// Status is "proposed" | "approved": teams that want the ceremony gate on
// approved; teams that do not, approve on write.
type Deviation struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Apps   [2]string `json:"apps"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Reason string    `json:"reason"`
}

// ToleratedNote is what a consumer sees on a difference a Deviation
// covered: the difference still happened and is still reported, it just
// does not count against the verdict. Never drop the difference itself —
// "tolerated" and "absent" must never look the same to a reviewer.
type ToleratedNote struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}
