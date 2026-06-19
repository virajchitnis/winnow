package store

import (
	"database/sql"
	"errors"
)

// Category describes how a class of mail is handled.
type Category struct {
	ID                int64
	Name              string
	DestinationFolder string // empty => stays in inbox
	KeepInInbox       bool
	Flag              bool
	MarkRead          bool
	IsBuiltin         bool
	SortOrder         int
}

// Moves reports whether mail in this category is moved out of the inbox.
func (c Category) Moves() bool { return c.DestinationFolder != "" && !c.KeepInInbox }

// presetCategories are the editable defaults seeded on first boot.
var presetCategories = []Category{
	{Name: "Important", KeepInInbox: true, Flag: true, IsBuiltin: true, SortOrder: 0},
	{Name: "Needs attention", KeepInInbox: true, Flag: true, IsBuiltin: true, SortOrder: 1},
	{Name: "Promotional", DestinationFolder: "Promotions", IsBuiltin: true, SortOrder: 2},
	{Name: "Social", DestinationFolder: "Social", IsBuiltin: true, SortOrder: 3},
	{Name: "Newsletters", DestinationFolder: "Reading", IsBuiltin: true, SortOrder: 4},
}

// SeedCategories inserts the preset categories if the table is empty.
func (s *Store) SeedCategories() error {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM categories").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, c := range presetCategories {
		if _, err := s.CreateCategory(c); err != nil {
			return err
		}
	}
	return nil
}

// Categories returns all categories ordered by sort order then name.
func (s *Store) Categories() ([]Category, error) {
	rows, err := s.db.Query(`
		SELECT id, name, destination_folder, keep_in_inbox, flag, mark_read, is_builtin, sort_order
		FROM categories ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CategoryByName returns a category by exact name.
func (s *Store) CategoryByName(name string) (Category, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, name, destination_folder, keep_in_inbox, flag, mark_read, is_builtin, sort_order
		FROM categories WHERE name = ?`, name)
	c, err := scanCategory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, false, nil
	}
	if err != nil {
		return Category{}, false, err
	}
	return c, true, nil
}

// CreateCategory inserts a category and returns its id.
func (s *Store) CreateCategory(c Category) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO categories(name, destination_folder, keep_in_inbox, flag, mark_read, is_builtin, sort_order)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.DestinationFolder, boolToInt(c.KeepInInbox), boolToInt(c.Flag),
		boolToInt(c.MarkRead), boolToInt(c.IsBuiltin), c.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateCategory updates an existing category by id.
func (s *Store) UpdateCategory(c Category) error {
	_, err := s.db.Exec(`
		UPDATE categories
		SET name = ?, destination_folder = ?, keep_in_inbox = ?, flag = ?, mark_read = ?, sort_order = ?
		WHERE id = ?`,
		c.Name, c.DestinationFolder, boolToInt(c.KeepInInbox), boolToInt(c.Flag),
		boolToInt(c.MarkRead), c.SortOrder, c.ID)
	return err
}

// DeleteCategory removes a non-builtin category by id.
func (s *Store) DeleteCategory(id int64) error {
	_, err := s.db.Exec("DELETE FROM categories WHERE id = ? AND is_builtin = 0", id)
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCategory(r scanner) (Category, error) {
	var c Category
	var keep, flag, mark, builtin int
	err := r.Scan(&c.ID, &c.Name, &c.DestinationFolder, &keep, &flag, &mark, &builtin, &c.SortOrder)
	if err != nil {
		return Category{}, err
	}
	c.KeepInInbox = intToBool(keep)
	c.Flag = intToBool(flag)
	c.MarkRead = intToBool(mark)
	c.IsBuiltin = intToBool(builtin)
	return c, nil
}
