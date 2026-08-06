package database

import (
	"context"
	"strings"
	"testing"
)

func TestInvalidDatabaseURLDoesNotExposeSecret(t *testing.T) {
	const secret = "super-secret-password"
	_, err := Open(context.Background(), "postgres://user:"+secret+"@%zz")
	if err == nil {
		t.Fatal("ожидалась ошибка некорректного TM_DATABASE_URL")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ошибка раскрыла пароль: %v", err)
	}
}
