package gormstore

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

type gormTestUser struct {
	ID    int64  `db:"id,pk,auto"`
	Name  string `db:"name"`
	Empty string `db:"empty,omitempty"`
}

func (gormTestUser) TableName() string {
	return "gorm_test_users"
}

type gormTestFallback struct {
	ID int64 `db:"id,pk,auto"`
}

func TestInsertColumnsOmitAutoAndEmpty(t *testing.T) {
	columns, err := insertColumns(gormTestUser{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"name"}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("columns = %#v, want %#v", columns, want)
	}
}

func TestStoreRejectsInvalidTableName(t *testing.T) {
	store := &Store[gormTestUser]{
		db:    &gorm.DB{},
		table: "users;drop",
	}
	_, err := store.List(context.Background())
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("err = %v, want ErrInvalidIdentifier", err)
	}
}

func TestNewStoreForResolvesTableName(t *testing.T) {
	if table := NewStoreFor[gormTestUser](nil).Table(); table != "gorm_test_users" {
		t.Fatalf("table=%q, want gorm_test_users", table)
	}
	if table := NewStoreFor[gormTestFallback](nil).Table(); table != "gorm_test_fallbacks" {
		t.Fatalf("fallback table=%q, want gorm_test_fallbacks", table)
	}
}

func TestResultLastInsertIDFromPrimaryKey(t *testing.T) {
	result := newResult(1, &gormTestUser{ID: 42})
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
}
