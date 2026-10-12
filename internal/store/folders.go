package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxFolderNameLength = 40

func ValidateFolderName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("folder name cannot be empty")
	}
	if !utf8.ValidString(name) {
		return errors.New("folder name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxFolderNameLength {
		return fmt.Errorf("folder name must be %d characters or fewer", MaxFolderNameLength)
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("folder name cannot begin with a hyphen")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("folder name cannot be %q", name)
	}
	for _, character := range name {
		if character == '/' || unicode.IsControl(character) {
			return errors.New("folder name cannot contain slashes or control characters")
		}
	}
	return nil
}

func (s *Store) CreateFolder(ctx context.Context, name, workspaceRoot string) (Folder, error) {
	name = strings.TrimSpace(name)
	if err := ValidateFolderName(name); err != nil {
		return Folder{}, err
	}
	folder := Folder{Name: name, WorkspaceRoot: workspaceRoot, CreatedAt: time.Now().UTC()}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO folders(name, workspace_root, created_at) VALUES (?, ?, ?)
ON CONFLICT(name) DO NOTHING`,
		folder.Name, folder.WorkspaceRoot, folder.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Folder{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("folder %q already exists", folder.Name)
		}
		return Folder{}, err
	}
	return folder, nil
}

func scanFolder(scanner interface{ Scan(...any) error }) (Folder, error) {
	var folder Folder
	var created string
	if err := scanner.Scan(&folder.Name, &folder.WorkspaceRoot, &created); err != nil {
		return Folder{}, err
	}
	var err error
	folder.CreatedAt, err = parseTime(created)
	return folder, err
}

func (s *Store) ListFolders(ctx context.Context) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, workspace_root, created_at FROM folders ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folders := []Folder{}
	for rows.Next() {
		folder, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}
	return folders, rows.Err()
}

func (s *Store) GetFolder(ctx context.Context, name string) (Folder, error) {
	folder, err := scanFolder(s.db.QueryRowContext(ctx,
		"SELECT name, workspace_root, created_at FROM folders WHERE name = ?",
		strings.TrimSpace(name)))
	if isNotFound(err) {
		return Folder{}, fmt.Errorf("folder %q not found", strings.TrimSpace(name))
	}
	return folder, err
}

// SetFolderWorkspace moves a folder to a new directory. Its jobs move with it,
// except running or paused jobs, which keep the directory they started in.
func (s *Store) SetFolderWorkspace(ctx context.Context, name, workspaceRoot string) (Folder, error) {
	name = strings.TrimSpace(name)
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Folder{}, err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx,
		"UPDATE folders SET workspace_root = ? WHERE name = ?", workspaceRoot, name)
	if err != nil {
		return Folder{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("folder %q not found", name)
		}
		return Folder{}, err
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE jobs SET workspace_root = ?
WHERE folder = ? AND status NOT IN (?, ?)`,
		workspaceRoot, name, JobRunning, JobWaitingInput); err != nil {
		return Folder{}, err
	}
	folder, err := scanFolder(transaction.QueryRowContext(ctx,
		"SELECT name, workspace_root, created_at FROM folders WHERE name = ?", name))
	if err != nil {
		return Folder{}, err
	}
	return folder, transaction.Commit()
}

// DeleteFolder removes a folder. Its jobs stay and become ungrouped.
func (s *Store) DeleteFolder(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, "DELETE FROM folders WHERE name = ?", name)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("folder %q not found", name)
		}
		return err
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE jobs SET folder = '' WHERE folder = ?", name); err != nil {
		return err
	}
	return transaction.Commit()
}
