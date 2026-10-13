package core

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Namespace groups conversations. Namespace `default` always exists.
type Namespace struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListNamespaces returns the namespaces the principal can read.
func (s *Service) ListNamespaces(ctx context.Context, p Principal) ([]Namespace, error) {
	all, err := collect(ctx, s.db, func(rows *sql.Rows) (Namespace, error) {
		var n Namespace
		var created int64
		err := rows.Scan(&n.Name, &created)
		n.CreatedAt = fromMS(created)
		return n, err
	}, `SELECT name, created_at FROM namespaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []Namespace{}
	for _, n := range all {
		if s.authorize(ctx, p, n.Name, ActionRead) == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// CreateNamespace creates a namespace.
func (s *Service) CreateNamespace(ctx context.Context, p Principal, name string) (Namespace, error) {
	if err := s.authorize(ctx, p, "", ActionAdmin); err != nil {
		return Namespace{}, err
	}
	if err := checkName("namespace", name); err != nil {
		return Namespace{}, err
	}
	now := s.now()
	err := s.writeAs(ctx, p, "", ActionAdmin, func(tx *wtx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO namespaces (name, created_at) VALUES (?, ?)`, name, ms(now))
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return errorf(CodeNamespaceExists, "namespace %q already exists", name)
		}
		return err
	})
	if err != nil {
		return Namespace{}, err
	}
	return Namespace{Name: name, CreatedAt: fromMS(ms(now))}, nil
}

func namespaceID(ctx context.Context, q querier, name string) (int64, error) {
	if err := checkName("namespace", name); err != nil {
		return 0, err
	}
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM namespaces WHERE name = ?`, name).Scan(&id)
	if isNoRows(err) {
		return 0, errorf(CodeNamespaceNotFound, "namespace %q not found", name)
	}
	return id, err
}
