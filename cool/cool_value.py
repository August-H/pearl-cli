"""
COOL Value System
Handles typed values and type checking for the COOL database engine.
"""

from cool_exceptions import DataError


class Value:
    """
    Represents a typed value in the COOL database system.
    Supports: int, string, float, boolean, null
    """

    VALID_TYPES = ('int', 'string', 'float', 'boolean', 'null')

    def __init__(self, value, value_type=None):
        """
        Create a new Value.

        Args:
            value: The raw value (or None for null)
            value_type: The type string ('int', 'string', 'float', 'boolean', 'null')
                       If None, it will be inferred from the value.
        """
        if value_type is None:
            value_type = self._infer_type(value)

        if value_type not in self.VALID_TYPES:
            raise DataError(f"Unknown type: {value_type}")

        self._type = value_type
        self._raw_value = value

    def _infer_type(self, value):
        """Infer the type of a Python value."""
        if value is None:
            return 'null'
        elif isinstance(value, bool):
            return 'boolean'
        elif isinstance(value, int):
            return 'int'
        elif isinstance(value, float):
            return 'float'
        elif isinstance(value, str):
            return 'string'
        else:
            raise DataError(f"Cannot infer type for value: {value}")

    @property
    def value(self):
        """Return the raw Python value."""
        return self._raw_value

    @property
    def type(self):
        """Return the COOL type string."""
        return self._type

    @classmethod
    def from_token(cls, token):
        """
        Create a Value from a lexer Token.

        Args:
            token: A Token object with .type and .value attributes

        Returns:
            A new Value instance
        """
        # Map token types to COOL types
        type_map = {
            'NUMBER_INT': 'int',
            'NUMBER_FLOAT': 'float',
            'STRING': 'string',
            'BOOLEAN': 'boolean',
            'NULL': 'null',
        }

        cool_type = type_map.get(token.type)
        if cool_type is None:
            raise DataError(f"Cannot create Value from token type: {token.type}")

        # Convert raw token value
        raw = token.value
        if cool_type == 'int':
            raw = int(raw)
        elif cool_type == 'float':
            raw = float(raw)
        elif cool_type == 'boolean':
            raw = (raw == 'true')

        return cls(raw, cool_type)

    def as_int(self):
        """Convert to int, with casting rules."""
        if self._type == 'int':
            return self._raw_value
        elif self._type == 'float':
            return int(self._raw_value)
        elif self._type == 'boolean':
            return 1 if self._raw_value else 0
        elif self._type == 'string':
            try:
                return int(self._raw_value)
            except ValueError:
                raise DataError(f"Cannot cast string '{self._raw_value}' to int")
        elif self._type == 'null':
            return None
        raise DataError(f"Cannot convert {self._type} to int")

    def as_float(self):
        """Convert to float, with casting rules."""
        if self._type in ('int', 'float'):
            return float(self._raw_value)
        elif self._type == 'boolean':
            return 1.0 if self._raw_value else 0.0
        elif self._type == 'string':
            try:
                return float(self._raw_value)
            except ValueError:
                raise DataError(f"Cannot cast string '{self._raw_value}' to float")
        elif self._type == 'null':
            return None
        raise DataError(f"Cannot convert {self._type} to float")

    def as_string(self):
        """Convert to string."""
        if self._type == 'string':
            return self._raw_value
        elif self._type is None or self._type == 'null':
            return None
        else:
            return str(self._raw_value)

    def as_boolean(self):
        """Convert to boolean."""
        if self._type == 'boolean':
            return self._raw_value
        elif self._type in ('int', 'float'):
            return bool(self._raw_value)
        elif self._type == 'string':
            if self._raw_value.lower() in ('true', '1', 'yes'):
                return True
            elif self._raw_value.lower() in ('false', '0', 'no'):
                return False
            raise DataError(f"Cannot cast string '{self._raw_value}' to boolean")
        elif self._type == 'null':
            return None
        raise DataError(f"Cannot convert {self._type} to boolean")

    def is_null(self):
        """Check if this value is null."""
        return self._type == 'null' or self._raw_value is None

    def __eq__(self, other):
        """Compare two values for equality."""
        if isinstance(other, Value):
            if self._type == 'null' or other.type == 'null':
                return self._type == other.type
            if self._type != other.type:
                return False
            return self._raw_value == other.value
        # Compare with Python primitive
        if self._type == 'int':
            return self._raw_value == other
        elif self._type == 'float':
            return self._raw_value == other
        elif self._type == 'string':
            return self._raw_value == other
        elif self._type == 'boolean':
            return self._raw_value == other
        return False

    def __lt__(self, other):
        """Less than comparison for sorting/filtering."""
        if isinstance(other, Value):
            if self._type == other.type and self._type in ('int', 'float', 'string'):
                return self._raw_value < other.value
            # Try numeric comparison
            if self._type in ('int', 'float') and other.type in ('int', 'float'):
                return self.as_float() < other.as_float()
            raise DataError(f"Cannot compare {self._type} with {other.type}")
        raise DataError(f"Cannot compare Value with {type(other)}")

    def __le__(self, other):
        """Less than or equal comparison."""
        return self == other or self < other

    def __gt__(self, other):
        """Greater than comparison."""
        if isinstance(other, Value):
            if self._type == other.type and self._type in ('int', 'float', 'string'):
                return self._raw_value > other.value
            if self._type in ('int', 'float') and other.type in ('int', 'float'):
                return self.as_float() > other.as_float()
            raise DataError(f"Cannot compare {self._type} with {other.type}")
        raise DataError(f"Cannot compare Value with {type(other)}")

    def __ge__(self, other):
        """Greater than or equal comparison."""
        return self == other or self > other

    def clone(self):
        """Create a copy of this value."""
        return Value(self._raw_value, self._type)

    def __repr__(self):
        if self._type == 'null':
            return 'NULL'
        elif self._type == 'string':
            return f"'{self._raw_value}'"
        else:
            return str(self._raw_value)

    def __str__(self):
        return self.__repr__()
