"""
COOL Token Definitions
Defines token types and the Token class for the COOL lexer.
"""

from collections import namedtuple


# Token is a named tuple with: type, value, line, column
Token = namedtuple('Token', ['type', 'value', 'line', 'column'])


# Token Type Constants
# Keywords
TOKEN_CREATE = 'CREATE'
TOKEN_TABLE = 'TABLE'
TOKEN_ALTER = 'ALTER'
TOKEN_DROP = 'DROP'
TOKEN_INSERT = 'INSERT'
TOKEN_INTO = 'INTO'
TOKEN_UPDATE = 'UPDATE'
TOKEN_DELETE = 'DELETE'
TOKEN_SELECT = 'SELECT'
TOKEN_FROM = 'FROM'
TOKEN_WHERE = 'WHERE'
TOKEN_VALUES = 'VALUES'
TOKEN_SET = 'SET'
TOKEN_JOIN = 'JOIN'
TOKEN_ON = 'ON'
TOKEN_AND = 'AND'
TOKEN_OR = 'OR'
TOKEN_NOT = 'NOT'

# Column constraint keywords
TOKEN_PRIMARY = 'PRIMARY'
TOKEN_KEY = 'KEY'
TOKEN_NULL = 'NULL'
TOKEN_REFERENCES = 'REFERENCES'
TOKEN_UNIQUE = 'UNIQUE'
TOKEN_DEFAULT = 'DEFAULT'

# Types
TOKEN_INT = 'INT'
TOKEN_STRING = 'STRING'
TOKEN_FLOAT = 'FLOAT'
TOKEN_BOOLEAN = 'BOOLEAN'

# Literals
TOKEN_NUMBER_INT = 'NUMBER_INT'
TOKEN_NUMBER_FLOAT = 'NUMBER_FLOAT'
TOKEN_STRING_LITERAL = 'STRING_LITERAL'
TOKEN_BOOLEAN_LITERAL = 'BOOLEAN'
TOKEN_NULL_LITERAL = 'NULL'

# Operators and delimiters
TOKEN_LPAREN = 'LPAREN'        # (
TOKEN_RPAREN = 'RPAREN'        # )
TOKEN_LBRACE = 'LBRACE'        # {
TOKEN_RBRACE = 'RBRACE'        # }
TOKEN_LBRACKET = 'LBRACKET'    # [
TOKEN_RBRACKET = 'RBRACKET'    # ]
TOKEN_COMMA = 'COMMA'          # ,
TOKEN_SEMICOLON = 'SEMICOLON'  # ;
TOKEN_DOT = 'DOT'              # .
TOKEN_STAR = 'STAR'            # *

# Comparison operators
TOKEN_EQ = 'EQ'                # =
TOKEN_NEQ = 'NEQ'              # !=
TOKEN_LT = 'LT'                # <
TOKEN_GT = 'GT'                # >
TOKEN_LE = 'LE'                # <=
TOKEN_GE = 'GE'                # >=

# Logical operators (already as keywords above: AND, OR, NOT)

# Assignment
TOKEN_ASSIGN = 'ASSIGN'        # -> (for updates)

# Identifier
TOKEN_IDENTIFIER = 'IDENTIFIER'

# End of file
TOKEN_EOF = 'EOF'


# Keyword mapping for lookup
KEYWORDS = {
    'create': TOKEN_CREATE,
    'table': TOKEN_TABLE,
    'alter': TOKEN_ALTER,
    'drop': TOKEN_DROP,
    'insert': TOKEN_INSERT,
    'into': TOKEN_INTO,
    'update': TOKEN_UPDATE,
    'delete': TOKEN_DELETE,
    'from': TOKEN_FROM,
    'where': TOKEN_WHERE,
    'values': TOKEN_VALUES,
    'set': TOKEN_SET,
    'join': TOKEN_JOIN,
    'on': TOKEN_ON,
    'and': TOKEN_AND,
    'or': TOKEN_OR,
    'not': TOKEN_NOT,
    'select': TOKEN_SELECT,
    'primary': TOKEN_PRIMARY,
    'key': TOKEN_KEY,
    'null': TOKEN_NULL,
    'references': TOKEN_REFERENCES,
    'unique': TOKEN_UNIQUE,
    'default': TOKEN_DEFAULT,
    'int': TOKEN_INT,
    'string': TOKEN_STRING,
    'float': TOKEN_FLOAT,
    'boolean': TOKEN_BOOLEAN,
    'true': TOKEN_BOOLEAN_LITERAL,
    'false': TOKEN_BOOLEAN_LITERAL,
}


def make_token(token_type, value, line, column):
    """Create a Token instance."""
    return Token(type=token_type, value=value, line=line, column=column)
