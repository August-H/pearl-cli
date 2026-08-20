package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	jobIDGenerationAttempts = 256
	MaxJobNameLength        = 20
)

var jobIDAdjectives = []string{
	"amber", "ancient", "apricot", "arctic", "ashen", "autumn", "azure", "blue",
	"bold", "breezy", "bright", "bronze", "calm", "cedar", "cherry", "clear",
	"clever", "coastal", "cobalt", "copper", "coral", "cosmic", "crisp", "dancing",
	"dawn", "deep", "desert", "dusty", "eager", "early", "eastern", "ebony",
	"ember", "emerald", "evergreen", "fair", "fern", "fiery", "floating", "floral",
	"forest", "fresh", "frosty", "gentle", "glacial", "golden", "grand", "green",
	"happy", "harbor", "hazel", "hidden", "hollow", "indigo", "iron", "ivory",
	"jade", "keen", "lake", "lemon", "light", "lively", "lucky", "lunar",
	"maple", "marine", "mellow", "midnight", "misty", "morning", "mossy", "mountain",
	"nimble", "noble", "northern", "ocean", "olive", "opal", "open", "orange",
	"patient", "peaceful", "peach", "pine", "plum", "polar", "quiet", "rapid",
	"raven", "red", "river", "rosy", "royal", "ruby", "sandy", "scarlet",
	"silver", "sky", "snowy", "solar", "spruce", "steady", "stone", "stormy",
	"sunny", "swift", "teal", "tender", "tidal", "timber", "tiny", "tranquil",
	"twilight", "velvet", "vivid", "warm", "western", "wild", "willow", "winter",
	"wise", "yellow", "young", "zephyr", "brisk", "cloudy", "crimson", "far",
	"lofty", "quietly", "radiant", "soft", "spring", "starlit", "summer", "woodland",
}

var jobIDNouns = []string{
	"acorn", "albatross", "anchor", "antler", "apple", "badger", "bay", "beacon",
	"bear", "birch", "bison", "blossom", "brook", "canyon", "cedar", "cliff",
	"cloud", "comet", "crane", "creek", "crow", "dawn", "delta", "dune",
	"eagle", "elm", "falcon", "feather", "field", "finch", "fir", "flame",
	"fox", "frost", "garden", "grove", "harbor", "hare", "hawk", "heron",
	"hill", "island", "ivy", "jay", "juniper", "kestrel", "lake", "lark",
	"leaf", "lynx", "maple", "meadow", "moon", "moss", "oak", "ocean",
	"orchid", "otter", "owl", "pebble", "pine", "plume", "pond", "poppy",
	"quail", "rain", "raven", "reef", "ridge", "river", "robin", "rock",
	"rose", "salmon", "sand", "seal", "shell", "shore", "sky", "snow",
	"sparrow", "spruce", "star", "stone", "storm", "stream", "summit", "sun",
	"tern", "tide", "tiger", "trail", "trout", "valley", "violet", "wave",
	"whale", "willow", "wind", "wolf", "wren", "aspen", "beech", "breeze",
	"cove", "cypress", "drift", "fern", "glade", "heath", "iris", "lagoon",
	"marsh", "mesa", "mist", "peak", "petal", "prairie", "rainbow", "reed",
	"sage", "shale", "slope", "thicket", "thunder", "timber", "vale", "wood",
}

func availableJobID(ctx context.Context, transaction *sql.Tx) (string, error) {
	for attempt := 0; attempt < jobIDGenerationAttempts; attempt++ {
		candidate, err := randomJobID()
		if err != nil {
			return "", fmt.Errorf("generate job id: %w", err)
		}
		var existing string
		err = transaction.QueryRowContext(
			ctx, "SELECT id FROM jobs WHERE id = ?", candidate,
		).Scan(&existing)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return candidate, nil
		case err != nil:
			return "", fmt.Errorf("check job id: %w", err)
		}
	}
	return "", fmt.Errorf("could not generate a unique two-word job id")
}

func ValidateJobName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("job name must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxJobNameLength {
		return fmt.Errorf("job name must be %d characters or fewer", MaxJobNameLength)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("job name cannot be %q", name)
	}
	for _, character := range name {
		if character == '/' || unicode.IsControl(character) {
			return fmt.Errorf("job name cannot contain slashes or control characters")
		}
	}
	return nil
}

func (s *Store) migrateStoredJobNames(ctx context.Context) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin stored job name migration: %w", err)
	}
	defer transaction.Rollback()

	rows, err := transaction.QueryContext(ctx, `
SELECT id, name FROM jobs WHERE TRIM(name) != '' ORDER BY created_at`)
	if err != nil {
		return fmt.Errorf("list stored job names: %w", err)
	}
	type namedJob struct {
		id   string
		name string
	}
	var jobs []namedJob
	for rows.Next() {
		var job namedJob
		if err := rows.Scan(&job.id, &job.name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan stored job name: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read stored job names: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close stored job names: %w", err)
	}

	for _, job := range jobs {
		customID := strings.TrimSpace(job.name)
		if err := ValidateJobName(customID); err != nil || customID == "" {
			if _, clearErr := transaction.ExecContext(
				ctx, "UPDATE jobs SET name = '' WHERE id = ?", job.id,
			); clearErr != nil {
				return fmt.Errorf("clear invalid stored job name for %q: %w", job.id, clearErr)
			}
			continue
		}
		if customID == job.id {
			if _, err := transaction.ExecContext(
				ctx, "UPDATE jobs SET name = '' WHERE id = ?", job.id,
			); err != nil {
				return fmt.Errorf("clear stored job name for %q: %w", job.id, err)
			}
			continue
		}
		var existing string
		err := transaction.QueryRowContext(
			ctx, "SELECT id FROM jobs WHERE id = ?", customID,
		).Scan(&existing)
		if err == nil {
			if _, clearErr := transaction.ExecContext(
				ctx, "UPDATE jobs SET name = '' WHERE id = ?", job.id,
			); clearErr != nil {
				return fmt.Errorf("clear duplicate stored job name for %q: %w", job.id, clearErr)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check stored job name %q: %w", customID, err)
		}
		if err := renameJobID(ctx, transaction, job.id, customID); err != nil {
			return fmt.Errorf("use stored name for job %q: %w", job.id, err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit stored job name migration: %w", err)
	}
	return nil
}

func (s *Store) migrateLegacyJobIDs(ctx context.Context) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy job ID migration: %w", err)
	}
	defer transaction.Rollback()

	rows, err := transaction.QueryContext(ctx, "SELECT id FROM jobs ORDER BY created_at")
	if err != nil {
		return fmt.Errorf("list job IDs for migration: %w", err)
	}
	var legacyIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan job ID for migration: %w", err)
		}
		if isLegacyJobID(id) {
			legacyIDs = append(legacyIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read job IDs for migration: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close job ID migration rows: %w", err)
	}

	for _, legacyID := range legacyIDs {
		newID, err := availableJobID(ctx, transaction)
		if err != nil {
			return fmt.Errorf("replace legacy job ID %q: %w", legacyID, err)
		}
		if err := renameJobID(ctx, transaction, legacyID, newID); err != nil {
			return fmt.Errorf("rename legacy job %q: %w", legacyID, err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit legacy job ID migration: %w", err)
	}
	return nil
}

func renameJobID(ctx context.Context, transaction *sql.Tx, oldID, newID string) error {
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO jobs (
    id, name, prompt, workspace_root, status, result, error, cancel_requested,
    transcript, input_question, input_tool_call_id, input_response,
    created_at, started_at, finished_at
)
SELECT
    ?, '', prompt, workspace_root, status, result, error, cancel_requested,
    transcript, input_question, input_tool_call_id, input_response,
    created_at, started_at, finished_at
FROM jobs WHERE id = ?`, newID, oldID); err != nil {
		return fmt.Errorf("copy job %q: %w", oldID, err)
	}
	if _, err := transaction.ExecContext(
		ctx, "UPDATE events SET job_id = ? WHERE job_id = ?", newID, oldID,
	); err != nil {
		return fmt.Errorf("move events for job %q: %w", oldID, err)
	}
	if _, err := transaction.ExecContext(
		ctx, "UPDATE tool_executions SET job_id = ? WHERE job_id = ?", newID, oldID,
	); err != nil {
		return fmt.Errorf("move tool results for job %q: %w", oldID, err)
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM jobs WHERE id = ?", oldID); err != nil {
		return fmt.Errorf("remove job %q: %w", oldID, err)
	}
	return nil
}

func isLegacyJobID(id string) bool {
	suffix, found := strings.CutPrefix(id, "job_")
	if !found || len(suffix) != 16 {
		return false
	}
	for _, character := range suffix {
		if !(character >= '0' && character <= '9') &&
			!(character >= 'a' && character <= 'f') &&
			!(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

func randomJobID() (string, error) {
	adjective, err := randomJobIDWord(jobIDAdjectives)
	if err != nil {
		return "", err
	}
	noun, err := randomJobIDWord(jobIDNouns)
	if err != nil {
		return "", err
	}
	return adjective + "-" + noun, nil
}

func randomJobIDWord(words []string) (string, error) {
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	if err != nil {
		return "", err
	}
	return words[index.Int64()], nil
}
