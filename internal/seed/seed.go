// Package seed holds first-run seed data embedded directly into the
// hapidays binary — data meant to travel with the install itself rather
// than needing a separate manual import step on every fresh deployment.
package seed

import _ "embed"

//go:embed smoke-test.collection.json
var SmokeTestCollection []byte
