"""
COOL Database Engine
Core database engine with tables, rows, and constraints.
"""

import json
from cool_exceptions import DatabaseError, TableError, ColumnError, DataError
from cool_value import Value


class Column:
    """
    Represents a column in a table with its definition and constraints.
    """

    def __init__(self, name, data_type, is_primary=False, is_nullable=True,
                 is_unique=False, default=None, references=None,
                 ordinal_position=0):
        self.name = name
        self.data_type = data_type
        self.is_primary = is_primary
        self.is_nullable = is_nullable
        self.is_unique = is_unique
        self.default = default  # Value object or None
        self.references = references  # (table_name, column_name) tuple or None
        self.ordinal_position = ordinal_position

    def validate_value(self, value):
        """
        Validate that a value is compatible with this column.

        Args:
            value: A Value object to validate

        Raises:
            DataError: If the value violates column constraints
        """
        # Check null constraint
        if value is None or value.is_null():
            if not self.is_nullable:
                raise DataError(
                    f"Column '{self.name}' does not allow NULL values"
                )
            return

        # Check type compatibility
        if value.type != self.data_type:
            raise DataError(
                f"Type mismatch in column '{self.name}': "
                f"expected {self.data_type}, got {value.type}"
            )

    def to_dict(self):
        """Serialize column metadata to a dictionary."""
        return {
            'name': self.name,
            'data_type': self.data_type,
            'is_primary': self.is_primary,
            'is_nullable': self.is_nullable,
            'is_unique': self.is_unique,
            'default': self.default.as_string() if self.default else None,
            'references': list(self.references) if self.references else None,
            'ordinal_position': self.ordinal_position,
        }

    @classmethod
    def from_dict(cls, data):
        """Deserialize column metadata from a dictionary."""
        default_val = None
        if data.get('default') is not None:
            if data['data_type'] == 'int':
                default_val = Value(int(data['default']), 'int')
            elif data['data_type'] == 'float':
                default_val = Value(float(data['default']), 'float')
            elif data['data_type'] == 'boolean':
                default_val = Value(data['default'] == 'true', 'boolean')
            else:
                default_val = Value(data['default'], 'string')

        refs = None
        if data.get('references'):
            refs = tuple(data['references'])

        return cls(
            name=data['name'],
            data_type=data['data_type'],
            is_primary=data['is_primary'],
            is_nullable=data['is_nullable'],
            is_unique=data['is_unique'],
            default=default_val,
            references=refs,
            ordinal_position=data.get('ordinal_position', 0),
        )

    def __repr__(self):
        attrs = [f"{self.name} ({self.data_type})"]
        if self.is_primary:
            attrs.append("PK")
        if not self.is_nullable:
            attrs.append("NOT NULL")
        if self.is_unique:
            attrs.append("UNIQUE")
        return " ".join(attrs)


class Row:
    """
    Represents a single row in a table.
    Columns are stored as a dict of column_name -> Value.
    """

    def __init__(self, values=None, columns=None):
        """
        Create a new row.

        Args:
            values: Dict of column_name -> Value, or None
            columns: List of column names (for ordering), or None
        """
        self._values = values or {}

    def set_value(self, column_name, value):
        """Set a value for a column."""
        self._values[column_name] = value

    def get_value(self, column_name):
        """Get the Value for a column, or None if not set."""
        return self._values.get(column_name)

    def has_column(self, column_name):
        """Check if this row has a value for the given column."""
        return column_name in self._values

    def get_dict(self):
        """Return a dict of column_name -> Value for this row."""
        return dict(self._values)

    def to_dict(self):
        """Serialize row to a dictionary for JSON storage."""
        result = {}
        for col, val in self._values.items():
            if val is None:
                result[col] = None
            else:
                result[col] = {'type': val.type, 'value': val.value}
        return result

    @classmethod
    def from_dict(cls, data):
        """Deserialize a row from a dictionary."""
        values = {}
        for col, val_data in data.items():
            if val_data is None:
                values[col] = Value(None, 'null')
            else:
                values[col] = Value(val_data['value'], val_data['type'])
        return cls(values=values)

    def __repr__(self):
        parts = []
        for col, val in self._values.items():
            parts.append(f"{col}={val}")
        return "{" + ", ".join(parts) + "}"


class Table:
    """
    Represents a table in the database with columns and rows.
    """

    def __init__(self, name):
        self.name = name
        self.columns = {}  # name -> Column
        self.column_order = []  # list of column names to preserve order
        self.rows = []  # list of Row objects
        self._primary_key = None  # Column name of primary key, if any

    # -----------------------------------------------------------------------
    # Schema operations
    # -----------------------------------------------------------------------

    def add_column(self, column):
        """
        Add a column to this table.

        Raises:
            ColumnError: If column name already exists
        """
        if column.name in self.columns:
            raise ColumnError(
                f"Column '{column.name}' already exists in table '{self.name}'"
            )
        column.ordinal_position = len(self.column_order)
        self.columns[column.name] = column
        self.column_order.append(column.name)

        if column.is_primary:
            self._primary_key = column.name

    def drop_column(self, column_name):
        """
        Remove a column from this table.

        Raises:
            ColumnError: If column doesn't exist
        """
        if column_name not in self.columns:
            raise ColumnError(
                f"Column '{column_name}' does not exist in table '{self.name}'"
            )
        del self.columns[column_name]
        self.column_order.remove(column_name)

        # Remove from rows
        for row in self.rows:
            row._values.pop(column_name, None)

        if self._primary_key == column_name:
            self._primary_key = None

    def get_column(self, column_name):
        """Get a Column object by name."""
        if column_name not in self.columns:
            raise ColumnError(
                f"Column '{column_name}' does not exist in table '{self.name}'"
            )
        return self.columns[column_name]

    @property
    def primary_key(self):
        """Return the name of the primary key column, or None."""
        return self._primary_key

    def get_primary_key_values(self, row):
        """Get the primary key value(s) from a row as a tuple."""
        if self._primary_key is None:
            return None
        pk_val = row.get_value(self._primary_key)
        return (pk_val.value,) if pk_val else (None,)

    # -----------------------------------------------------------------------
    # Data operations
    # -----------------------------------------------------------------------

    def insert(self, column_names, values):
        """
        Insert a new row into the table.

        Args:
            column_names: List of column names
            values: List of Value objects

        Raises:
            ColumnError: If column doesn't exist
            DataError: If value validation fails (type mismatch, NULL constraint, etc.)
            TableError: If unique constraint or primary key uniqueness is violated
        """
        # Check primary key uniqueness
        if self._primary_key:
            pk_column = self.columns[self._primary_key]
            pk_index = None
            pk_value = None
            for i, col_name in enumerate(column_names):
                if col_name == self._primary_key:
                    pk_value = values[i]
                    break
            if pk_value is not None:
                if not pk_value.is_null():
                    for existing_row in self.rows:
                        existing_pk = existing_row.get_value(self._primary_key)
                        if existing_pk and existing_pk == pk_value:
                            raise TableError(
                                f"Primary key constraint violation: "
                                f"value '{pk_value}' already exists in table '{self.name}'"
                            )

        # Validate and build row
        row = Row()

        # First, set all provided values
        for i, col_name in enumerate(column_names):
            if col_name not in self.columns:
                raise ColumnError(
                    f"Column '{col_name}' does not exist in table '{self.name}'"
                )
            col = self.columns[col_name]
            value = values[i]

            col.validate_value(value)

            # Check unique constraint
            if col.is_unique and not value.is_null():
                for existing_row in self.rows:
                    existing = existing_row.get_value(col_name)
                    if existing and existing == value:
                        raise TableError(
                            f"Unique constraint violation: "
                            f"value '{value}' already exists in column '{col_name}'"
                        )

            row.set_value(col_name, value)

        # Fill in defaults for columns not provided
        for col_name in self.column_order:
            if not row.has_column(col_name):
                col = self.columns[col_name]
                if col.is_nullable:
                    row.set_value(col_name, Value(None, 'null'))
                elif col.default is not None:
                    row.set_value(col_name, col.default.clone())
                elif col.is_primary:
                    # Primary key without default - set to null (will fail later if NULL)
                    row.set_value(col_name, Value(None, 'null'))

        self.rows.append(row)

    def select_all(self, column_refs=None, where_clause=None):
        """
        Select rows from the table, optionally filtered by a WHERE clause.

        Args:
            column_refs: List of column names to return (None = all)
            where_clause: Function(row) -> bool or None

        Returns:
            List of Row objects (copied)
        """
        result = []

        for row in self.rows:
            if where_clause is not None and not where_clause(row):
                continue

            if column_refs is None or '*' in column_refs:
                # Return a copy with all columns
                result.append(Row(dict(row._values)))
            else:
                # Return a copy with only specified columns
                new_row = Row()
                for col_name in column_refs:
                    if col_name not in self.columns:
                        raise ColumnError(
                            f"Column '{col_name}' does not exist in table '{self.name}'"
                        )
                    val = row.get_value(col_name)
                    if val:
                        new_row.set_value(col_name, val.clone())
                    else:
                        new_row.set_value(col_name, Value(None, 'null'))
                result.append(new_row)

        return result

    def update(self, assignments, where_clause=None):
        """
        Update rows in the table.

        Args:
            assignments: Dict of column_name -> Value
            where_clause: Function(row) -> bool, or None for all rows

        Returns:
            Number of rows updated
        """
        count = 0
        for row in self.rows:
            if where_clause is not None and not where_clause(row):
                continue

            for col_name, value in assignments.items():
                if col_name not in self.columns:
                    raise ColumnError(
                        f"Column '{col_name}' does not exist in table '{self.name}'"
                    )

                col = self.columns[col_name]
                col.validate_value(value)

                # Check unique constraint
                if col.is_unique and not value.is_null():
                    for other_row in self.rows:
                        if other_row is row:
                            continue
                        existing = other_row.get_value(col_name)
                        if existing and existing == value:
                            raise TableError(
                                f"Unique constraint violation: "
                                f"value '{value}' already exists in column '{col_name}'"
                            )

                row.set_value(col_name, value.clone())

            count += 1

        return count

    def delete(self, where_clause=None):
        """
        Delete rows from the table.

        Args:
            where_clause: Function(row) -> bool, or None for all rows

        Returns:
            Number of rows deleted
        """
        if where_clause is None:
            count = len(self.rows)
            self.rows = []
            return count

        to_delete = []
        keep = []
        for row in self.rows:
            if where_clause(row):
                to_delete.append(row)
            else:
                keep.append(row)
        self.rows = keep
        return len(to_delete)

    # -----------------------------------------------------------------------
    # Serialization
    # -----------------------------------------------------------------------

    def to_dict(self):
        """Serialize the table schema and data to a dictionary."""
        return {
            'name': self.name,
            'columns': {col_name: col.to_dict() for col_name, col in self.columns.items()},
            'column_order': self.column_order,
            'rows': [row.to_dict() for row in self.rows],
        }

    @classmethod
    def from_dict(cls, data):
        """Deserialize a table from a dictionary."""
        table = cls(data['name'])
        for col_name in data.get('column_order', []):
            col_data = data['columns'][col_name]
            col = Column.from_dict(col_data)
            table.add_column(col)
        for row_data in data.get('rows', []):
            row = Row.from_dict(row_data)
            table.rows.append(row)
        return table

    def get_row_count(self):
        """Return the number of rows in this table."""
        return len(self.rows)

    def __repr__(self):
        return f"Table('{self.name}', {len(self.column_order)} cols, {len(self.rows)} rows)"


class Database:
    """
    Manages a collection of tables and their relationships.
    """

    def __init__(self, name="main"):
        self.name = name
        self.tables = {}  # name -> Table

    def create_table(self, table_name, column_defs):
        """
        Create a new table.

        Args:
            table_name: Name of the table
            column_defs: List of ColumnDef objects from the AST

        Raises:
            TableError: If table already exists or there are schema issues
        """
        if table_name in self.tables:
            raise TableError(f"Table '{table_name}' already exists")

        table = Table(table_name)

        # Convert ColumnDef objects to Column objects
        for col_def in column_defs:
            col = Column(
                name=col_def.name,
                data_type=col_def.data_type,
                is_primary=col_def.is_primary,
                is_nullable=col_def.is_nullable,
                is_unique=col_def.is_unique,
                default=col_def.default,
                references=col_def.references,
            )
            table.add_column(col)

        # Check for primary key
        has_pk = any(c.is_primary for c in column_defs)
        if not has_pk:
            # No explicit primary key, that's okay

        # Validate foreign key references
        for col_def in column_defs:
            if col_def.references:
                ref_table, ref_col = col_def.references
                if ref_table not in self.tables:
                    raise TableError(
                        f"Cannot create table: referenced table '{ref_table}' "
                        f"does not exist"
                    )
                if ref_col not in [c.name for c in self.tables[ref_table].column_order]:
                    raise TableError(
                        f"Cannot create table: referenced column '{ref_col}' "
                        f"does not exist in table '{ref_table}'"
                    )

        self.tables[table_name] = table

    def drop_table(self, table_name):
        """Drop a table from the database."""
        if table_name not in self.tables:
            raise TableError(f"Table '{table_name}' does not exist")
        del self.tables[table_name]

    def alter_table_add_column(self, table_name, column_def):
        """Add a column to an existing table."""
        if table_name not in self.tables:
            raise TableError(f"Table '{table_name}' does not exist")

        table = self.tables[table_name]
        col = Column(
            name=column_def.name,
            data_type=column_def.data_type,
            is_primary=column_def.is_primary,
            is_nullable=column_def.is_nullable,
            is_unique=column_def.is_unique,
            default=column_def.default,
            references=column_def.references,
        )
        table.add_column(col)

    def alter_table_drop_column(self, table_name, column_name):
        """Drop a column from an existing table."""
        if table_name not in self.tables:
            raise TableError(f"Table '{table_name}' does not exist")
        self.tables[table_name].drop_column(column_name)

    def get_table(self, table_name):
        """Get a Table object by name."""
        if table_name not in self.tables:
            raise TableError(f"Table '{table_name}' does not exist")
        return self.tables[table_name]

    def table_exists(self, table_name):
        """Check if a table exists."""
        return table_name in self.tables

    def list_tables(self):
        """Return a list of table names."""
        return list(self.tables.keys())

    # -----------------------------------------------------------------------
    # Serialization
    # -----------------------------------------------------------------------

    def to_dict(self):
        """Serialize the entire database to a dictionary."""
        return {
            'name': self.name,
            'tables': {name: table.to_dict() for name, table in self.tables.items()},
        }

    @classmethod
    def from_dict(cls, data):
        """Deserialize a database from a dictionary."""
        db = cls(data.get('name', 'main'))
        for table_name, table_data in data.get('tables', {}).items():
            table = Table.from_dict(table_data)
            db.tables[table_name] = table
        return db

    def to_json(self):
        """Serialize the database to a JSON string."""
        return json.dumps(self.to_dict(), indent=2)

    @classmethod
    def from_json(cls, json_str):
        """Deserialize a database from a JSON string."""
        data = json.loads(json_str)
        return cls.from_dict(data)

    def save_to_file(self, filepath):
        """Save the database to a JSON file."""
        with open(filepath, 'w') as f:
            f.write(self.to_json())

    @classmethod
    def load_from_file(cls, filepath):
        """Load a database from a JSON file."""
        try:
            with open(filepath, 'r') as f:
                json_str = f.read()
            return cls.from_json(json_str)
        except FileNotFoundError:
            return cls()

    def __repr__(self):
        return f"Database('{self.name}', {len(self.tables)} tables)"
