// File: internal/modules/users/model.go
// Purpose: Defines the user domain model.

package users

import (
	"time"
)

// TableName is the users source-of-truth table.
const TableName = "users"

// User is the users module domain entity.
type User struct {
	ID        int64
	Name      string
	Age       int
	CreatedAt time.Time
}
