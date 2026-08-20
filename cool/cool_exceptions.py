"""
COOL Language Exceptions
Defines all error types used throughout the COOL system.
"""


class CoolError(Exception):
    """Base exception for all COOL-related errors."""
    pass


class LexerError(CoolError):
    """Raised when the lexer encounters an invalid token."""
    pass


class ParserError(CoolError):
    """Raised when the parser encounters invalid syntax."""
    pass


class ExecutionError(CoolError):
    """Raised when there's an error during statement execution."""
    pass


class DatabaseError(ExecutionError):
    """Raised when there's an error at the database level (table not found, etc.)"""
    pass


class TableError(ExecutionError):
    """Raised when there's an error at the table level."""
    pass


class ColumnError(ExecutionError):
    """Raised when there's an error related to columns."""
    pass


class DataError(ExecutionError):
    """Raised when there's an error in the data itself."""
    pass
