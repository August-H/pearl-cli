"""
COOL Lexer
Converts COOL source code into a stream of tokens.
"""

import re
from cool_token import *
from cool_exceptions import LexerError


class CoolLexer:
    """
    Tokenizes COOL source code into a list of Token objects.

    Usage:
        lexer = CoolLexer("create table users { ... }")
        tokens = lexer.tokenize()
    """

    # Token specification as regex patterns
    # Order matters - more specific patterns should come first
    TOKEN_PATTERNS = [
        # Whitespace and newlines (not returned as tokens)
        (r'\s+',                  None),  # Skip whitespace
        (r'//.*?$',               None),  # Skip single-line comments (with DOTALL)

        # Literals
        (r'\d+\.\d+',             TOKEN_NUMBER_FLOAT),
        (r'\d+',                  TOKEN_NUMBER_INT),
        (r'"[^"]*"',             TOKEN_STRING_LITERAL),  # "text"
        (r"'[^']*'",             TOKEN_STRING_LITERAL),  # 'text'

        # Multi-character operators (must come before single-char)
        (r'!=',                   TOKEN_NEQ),
        (r'<=',                   TOKEN_LE),
        (r'>=',                   TOKEN_GE),
        (r'->',                   TOKEN_ASSIGN),

        # Single-character operators and delimiters
        (r'=',                    TOKEN_EQ),
        (r'<',                    TOKEN_LT),
        (r'>',                    TOKEN_GT),
        (r'\(',                   TOKEN_LPAREN),
        (r'\)',                   TOKEN_RPAREN),
        (r'\{',                   TOKEN_LBRACE),
        (r'\}',                   TOKEN_RBRACE),
        (r'\[',                   TOKEN_LBRACKET),
        (r'\]',                   TOKEN_RBRACKET),
        (r',',                    TOKEN_COMMA),
        (r';',                    TOKEN_SEMICOLON),
        (r'\.',                   TOKEN_DOT),
        (r'\*',                   TOKEN_STAR),

        # Identifiers and keywords
        (r'[a-zA-Z_][a-zA-Z0-9_]*', 'IDENTIFIER_OR_KEYWORD'),
    ]

    def __init__(self, source_code):
        """
        Initialize the lexer with source code.

        Args:
            source_code: A string containing COOL source code
        """
        self.source_code = source_code
        self.tokens = []
        self._line = 1
        self._column = 0
        self._pos = 0
        self._source_len = len(source_code)

    def _make_pattern(self):
        """Compile all token patterns into a single regex."""
        parts = []
        for pattern, token_type in self.TOKEN_PATTERNS:
            if token_type is None:
                parts.append(f'(?P<SKIP>{pattern})')
            elif token_type == 'IDENTIFIER_OR_KEYWORD':
                parts.append(f'(?P<ID>{pattern})')
            else:
                parts.append(f'(?P<{token_type}>{pattern})')

        # Build the regex with named groups
        names = [t for p, t in self.TOKEN_PATTERNS if t is not None]
        # For comments, we need DOTALL
        regex_str = '|'.join(parts)
        return re.compile(regex_str, re.VERBOSE | re.DOTALL)

    def tokenize(self):
        """
        Tokenize the source code.

        Returns:
            A list of Token objects

        Raises:
            LexerError: If an invalid token is encountered
        """
        self.tokens = []
        self._line = 1
        self._column = 0
        self._pos = 0

        # Build individual regex objects for each pattern
        compiled_patterns = []
        for pattern, token_type in self.TOKEN_PATTERNS:
            if token_type is None:
                # Skip pattern (whitespace, comments)
                compiled_patterns.append((re.compile(pattern, re.DOTALL), None))
            else:
                compiled_patterns.append((re.compile(pattern), token_type))

        while self._pos < self._source_len:
            matched = False

            for regex, token_type in compiled_patterns:
                match = regex.match(self.source_code, self._pos)
                if match:
                    matched = True
                    value = match.group(0)

                    if token_type is None:
                        # Skip whitespace/comments
                        self._advance_position(value)
                        break
                    elif token_type == 'IDENTIFIER_OR_KEYWORD':
                        # Check if it's a keyword or identifier
                        lower_val = value.lower()
                        if lower_val in KEYWORDS:
                            token_type = KEYWORDS[lower_val]
                            if token_type == TOKEN_BOOLEAN_LITERAL:
                                # true/false are stored as their string value
                                pass
                            elif token_type == TOKEN_NULL_LITERAL:
                                value = 'null'
                        else:
                            token_type = TOKEN_IDENTIFIER

                    # Create token
                    self.tokens.append(make_token(token_type, value, self._line, self._column))
                    self._advance_position(value)
                    break

            if not matched:
                # No pattern matched - error
                char = self.source_code[self._pos]
                raise LexerError(
                    f"Unexpected character '{char}' at line {self._line}, column {self._column}"
                )

        # Add EOF token
        self.tokens.append(make_token(TOKEN_EOF, '', self._line, self._column))
        return self.tokens

    def _advance_position(self, value):
        """Update line/column position tracking."""
        for char in value:
            if char == '\n':
                self._line += 1
                self._column = 0
            else:
                self._column += 1
            self._pos += 1

    @property
    def token_count(self):
        """Return the number of tokens."""
        return len(self.tokens)
