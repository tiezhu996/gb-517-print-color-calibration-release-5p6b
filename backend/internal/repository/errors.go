package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// isDuplicateKey detects unique-constraint failures across the supported MySQL
// and SQLite (test/development) drivers.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "unique constraint failed") ||
		strings.Contains(message, "(1555)") || strings.Contains(message, "(2067)")
}
