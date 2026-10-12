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

// Job ID words are lowercase letters only, at most jobIDMaxWordLength long, so
// every generated ID is two hyphen-separated words that fit the job ID column.
const jobIDMaxWordLength = 9

var jobIDAdjectives = []string{
	"abundant", "adept", "agile", "airy", "alert", "alpine", "amber", "amiable",
	"ample", "ancient", "antique", "apricot", "aqua", "arctic", "ardent", "artful",
	"ashen", "astral", "auburn", "autumn", "avid", "awake", "azure",
	"balmy", "beige", "black", "blissful", "blithe", "blooming", "blue", "blushing",
	"bold", "bonny", "boreal", "bouncy", "boundless", "bountiful", "brass", "brave",
	"breezy", "bright", "brilliant", "brisk", "bronze", "brown", "bubbly", "budding",
	"buoyant", "burgundy", "busy", "buttery", "buzzing",
	"calm", "candid", "careful", "caring", "celestial", "charcoal", "charming", "cheerful",
	"cheery", "cherry", "chestnut", "chiming", "chipper", "cinnamon", "citrine", "citrus",
	"civil", "classic", "classy", "clever", "cloudy", "coastal", "cobalt", "copper",
	"coral", "cordial", "cosmic", "cozy", "crafty", "cream", "crimson", "crisp",
	"crystal", "curious", "curly", "cyan",
	"dainty", "dancing", "dapper", "daring", "dawn", "dazzling", "deep", "deft",
	"deluxe", "denim", "desert", "devoted", "dewy", "diligent", "dreamy", "drifting",
	"dusky", "dusty", "dynamic",
	"eager", "early", "earnest", "earthy", "eastern", "easy", "ebony", "echoing",
	"elder", "electric", "elegant", "elfin", "ember", "emerald", "enchanted", "epic",
	"eternal", "evening", "evergreen", "exact", "exotic",
	"fabled", "fabulous", "fair", "faithful", "famous", "fancy", "fawn", "fearless",
	"feathery", "fervent", "festive", "fiery", "fierce", "fine", "firm", "flashy",
	"fleet", "floating", "floral", "flowery", "fluent", "fluffy", "flying", "focused",
	"foggy", "fond", "forest", "frank", "free", "fresh", "friendly", "frosted",
	"frosty", "frozen", "fruity", "frugal", "furry", "fuzzy",
	"galactic", "gallant", "garnet", "gentle", "genuine", "giant", "giddy", "gifted",
	"gilded", "ginger", "glad", "glacial", "gleaming", "gleeful", "gliding", "glinting",
	"glorious", "glossy", "glowing", "golden", "good", "graceful", "gracious", "grand",
	"granite", "grassy", "grateful", "gray", "great", "green", "gritty",
	"happy", "harbor", "hardy", "hazel", "hazy", "hearty", "heavenly", "helpful",
	"heroic", "hidden", "hollow", "honest", "honey", "honeyed", "hopeful", "humble",
	"humming", "hushed",
	"icy", "iconic", "ideal", "idyllic", "indigo", "infinite", "intrepid", "iron",
	"island", "ivory",
	"jade", "jaunty", "jazzy", "jeweled", "jolly", "jovial", "joyful", "joyous",
	"jubilant", "juicy", "jungle", "just",
	"keen", "khaki", "kind", "kindly", "kinetic", "kingly",
	"lakeside", "laughing", "lavender", "lavish", "leafy", "leaping", "legendary", "lemon",
	"lilac", "lilting", "limber", "lime", "linen", "lithe", "little", "lively",
	"lofty", "lovely", "loyal", "lucid", "lucky", "luminous", "lunar", "lush",
	"lyrical",
	"magenta", "magic", "mahogany", "majestic", "maple", "marble", "marine", "maroon",
	"mauve", "meadow", "mellow", "merry", "midnight", "mighty", "mindful", "minty",
	"misty", "modern", "modest", "morning", "mossy", "mountain", "mustard", "mystic",
	"mythic",
	"natural", "nautical", "navy", "neat", "nifty", "nimble", "noble", "nomadic",
	"northern", "novel", "nutty",
	"oaken", "ocean", "ochre", "olden", "olive", "onyx", "opal", "orange",
	"orbital", "ornate",
	"pastoral", "patient", "peach", "pearl", "peppery", "peppy", "perky", "pewter",
	"pink", "placid", "platinum", "playful", "pleasant", "plucky", "plum", "plush",
	"poetic", "poised", "polar", "polite", "prairie", "precise", "prime", "pristine",
	"proud", "prudent", "purple",
	"quaint", "quantum", "quartz", "quick", "quiet", "quirky",
	"radiant", "rainy", "rapid", "rare", "ready", "red", "regal", "restful",
	"rhythmic", "ripe", "river", "robust", "rocky", "rolling", "roaming", "rose",
	"rosy", "round", "roving", "royal", "ruby", "rugged", "rushing", "russet",
	"rust", "rustic",
	"saffron", "sage", "sailing", "salmon", "salty", "sandy", "sapphire", "satin",
	"savory", "savvy", "scarlet", "scenic", "seaside", "secret", "sepia", "serene",
	"shady", "sharp", "shining", "shiny", "sienna", "silent", "silken", "silky",
	"silver", "simple", "sincere", "singing", "slate", "sleek", "sleepy", "smart",
	"smoky", "smooth", "snappy", "snowy", "snug", "soaring", "soft", "solar",
	"solid", "sonic", "sound", "southern", "sparkly", "speedy", "spicy", "spinning",
	"spirited", "splendid", "sporty", "spring", "spry", "stable", "starlit", "starry",
	"stately", "steady", "steely", "stellar", "sterling", "stoic", "stormy", "storied",
	"stout", "sturdy", "sublime", "subtle", "sugary", "summer", "sunlit", "sunny",
	"superb", "supreme", "sure", "sweeping", "sweet", "swift", "swirling", "sylvan",
	"tactful", "tall", "tan", "tangy", "tawny", "teal", "tender", "thrifty",
	"thriving", "tidal", "tidy", "timber", "timeless", "timely", "tiny", "tireless",
	"titanium", "toasty", "topaz", "tough", "tranquil", "treasured", "tropical", "true",
	"trusty", "tundra", "turquoise", "twilight", "twirling",
	"umber", "upbeat", "urban", "utopian",
	"valiant", "valley", "vanilla", "vast", "velvet", "velvety", "verdant", "vernal",
	"vibrant", "vigilant", "vintage", "violet", "vital", "vivid",
	"wandering", "warm", "western", "whimsical", "whistling", "white", "wholesome", "wide",
	"wild", "willow", "windswept", "windy", "winding", "winter", "wintry", "wise",
	"witty", "wondrous", "wooden", "woodland", "woolen", "worldly", "worthy", "woven",
	"yellow", "young", "youthful",
	"zany", "zealous", "zen", "zephyr", "zesty", "zippy",
}

var jobIDNouns = []string{
	// Animals
	"albatross", "alpaca", "antelope", "armadillo", "badger", "barracuda", "beaver", "bee",
	"beetle", "bison", "bluebird", "bobcat", "buffalo", "bunny", "butterfly", "camel",
	"canary", "caribou", "cheetah", "chickadee", "chipmunk", "cicada", "cobra", "condor",
	"cougar", "coyote", "crab", "crane", "cricket", "crow", "cuckoo", "curlew",
	"deer", "dingo", "dolphin", "donkey", "dove", "dragon", "eagle", "egret",
	"elk", "emu", "ermine", "falcon", "ferret", "finch", "firefly", "flamingo",
	"fox", "gazelle", "gecko", "gibbon", "giraffe", "goose", "gopher", "gorilla",
	"grouse", "gull", "hare", "hawk", "hedgehog", "heron", "hippo", "hornet",
	"horse", "hound", "ibex", "ibis", "iguana", "impala", "jackal", "jaguar",
	"jay", "kangaroo", "kestrel", "kite", "kiwi", "koala", "koi", "lark",
	"lemur", "leopard", "lion", "lizard", "llama", "lobster", "loon", "lynx",
	"macaw", "magpie", "mallard", "manatee", "mantis", "marlin", "marmot", "marten",
	"meerkat", "merlin", "mink", "mongoose", "moose", "moth", "mustang", "narwhal",
	"newt", "nighthawk", "ocelot", "octopus", "orca", "oriole", "osprey", "ostrich",
	"otter", "owl", "oyster", "panda", "panther", "parrot", "partridge", "peacock",
	"pelican", "penguin", "petrel", "pheasant", "pigeon", "pika", "plover", "pony",
	"porpoise", "puffin", "puma", "python", "quail", "quokka", "rabbit", "raccoon",
	"raven", "robin", "rooster", "sable", "salmon", "sandpiper", "seahorse", "seal",
	"shark", "shrike", "skylark", "sloth", "snail", "sparrow", "squid", "squirrel",
	"stag", "starling", "stingray", "stork", "swallow", "swan", "swift", "tapir",
	"tern", "thrush", "tiger", "toucan", "tortoise", "trout", "tuna", "turtle",
	"viper", "vole", "walrus", "warbler", "weasel", "whale", "wolf", "wombat",
	"wren", "yak", "zebra",
	// Plants
	"acorn", "almond", "aloe", "amaryllis", "anemone", "apple", "apricot", "aspen",
	"azalea", "bamboo", "banyan", "basil", "bay", "beech", "berry", "birch",
	"blossom", "bluebell", "bramble", "briar", "buttercup", "cactus", "camellia", "carnation",
	"cedar", "cherry", "chestnut", "clover", "cypress", "daffodil", "dahlia", "daisy",
	"elm", "fern", "fig", "fir", "foxglove", "gardenia", "geranium", "ginkgo",
	"hawthorn", "hazel", "heather", "hibiscus", "holly", "hyacinth", "iris", "ivy",
	"jasmine", "juniper", "kelp", "laurel", "lavender", "lemon", "lilac", "lily",
	"linden", "lotus", "magnolia", "mango", "maple", "marigold", "mimosa", "mint",
	"moss", "myrtle", "nutmeg", "oak", "olive", "orchid", "palm", "pansy",
	"peach", "pear", "peony", "pepper", "petunia", "pine", "plum", "poplar",
	"poppy", "primrose", "pumpkin", "quince", "redwood", "reed", "rose", "rosemary",
	"saffron", "sage", "sequoia", "sorrel", "spruce", "sunflower", "sycamore", "tamarind",
	"thistle", "thyme", "tulip", "violet", "walnut", "wheat", "willow", "wisteria",
	"yarrow", "yew", "zinnia",
	// Places
	"arch", "atoll", "avalanche", "basin", "beach", "bluff", "bog", "boulder",
	"brook", "butte", "canyon", "cape", "cascade", "cave", "cavern", "cliff",
	"coast", "cove", "crag", "crater", "creek", "crest", "delta", "dell",
	"desert", "dune", "estuary", "field", "fjord", "forest", "geyser", "glacier",
	"glade", "glen", "gorge", "grotto", "grove", "gulch", "gully", "harbor",
	"heath", "highland", "hill", "hollow", "inlet", "island", "isle", "jungle",
	"knoll", "lagoon", "lake", "ledge", "marsh", "meadow", "mesa", "moor",
	"mountain", "oasis", "ocean", "orchard", "pasture", "peak", "pebble", "plain",
	"plateau", "pond", "prairie", "quarry", "rapids", "ravine", "reef", "ridge",
	"river", "rock", "savanna", "shoal", "shore", "slope", "spring", "stream",
	"summit", "swamp", "terrace", "thicket", "tide", "trail", "tundra", "valley",
	"vista", "volcano", "waterfall", "wetland", "woods",
	// Sky and weather
	"aurora", "blizzard", "breeze", "cloud", "comet", "cosmos", "dawn", "dew",
	"drizzle", "dusk", "eclipse", "ember", "flame", "flare", "fog", "frost",
	"gale", "galaxy", "glow", "hail", "haze", "horizon", "lightning", "meteor",
	"mist", "moon", "moonbeam", "nebula", "nova", "orbit", "planet", "prism",
	"pulsar", "quasar", "rain", "rainbow", "shadow", "sky", "snow", "snowflake",
	"solstice", "spark", "star", "starlight", "storm", "sun", "sunbeam", "sunrise",
	"sunset", "tempest", "thunder", "twilight", "typhoon", "vapor", "wind", "zenith",
	// Things
	"anchor", "anvil", "arrow", "atlas", "badge", "banner", "beacon", "bell",
	"bridge", "bugle", "button", "cabin", "candle", "canoe", "canvas", "castle",
	"chime", "citadel", "clock", "compass", "cottage", "crown", "crystal", "drum",
	"engine", "feather", "flute", "fountain", "gadget", "garden", "gate", "gem",
	"harp", "helm", "horn", "jewel", "journal", "kayak", "kettle", "key",
	"lantern", "ledger", "lens", "locket", "loom", "lute", "map", "marble",
	"medal", "mirror", "mosaic", "needle", "oar", "orb", "paddle", "palette",
	"pearl", "pendant", "pillar", "pixel", "plume", "pocket", "quill", "quiver",
	"raft", "ribbon", "rocket", "rudder", "saddle", "sail", "satchel", "scroll",
	"sextant", "shell", "shield", "signal", "sketch", "sled", "sonnet", "spindle",
	"spire", "tablet", "teapot", "telescope", "temple", "thimble", "thread", "tower",
	"trumpet", "tunnel", "vessel", "violin", "voyage", "wagon", "whistle", "window",
	// Food
	"bagel", "biscuit", "brownie", "cocoa", "cookie", "cupcake", "ginger", "honey",
	"muffin", "noodle", "pancake", "pickle", "pretzel", "pudding", "radish", "scone",
	"sorbet", "toffee", "truffle", "waffle",
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
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("job name cannot begin with a hyphen")
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
    transcript, agent_context, input_question, input_tool_call_id, input_response,
    created_at, started_at, finished_at, harness, folder
)
SELECT
    ?, '', prompt, workspace_root, status, result, error, cancel_requested,
    transcript, agent_context, input_question, input_tool_call_id, input_response,
    created_at, started_at, finished_at, harness, folder
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
