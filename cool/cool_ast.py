"""
COOL Abstract Syntax Tree (AST)
Defines node types for representing parsed COOL statements.
"""

from cool_token import Token


class ASTNode:
    """Base class for all AST nodes."""
    pass


class Statement(ASTNode):
    """Base class for all statements."""
    pass


# ---------------------------------------------------------------------------
# DDL (Data Definition Language) Statements
# ---------------------------------------------------------------------------

class ColumnDef:
    """
    Definition of a column in a CREATE TABLE statement.

    Attributes:
        name: Column name (string)
        data_type: One of 'int', 'string', 'float', 'boolean'
        is_primary: Boolean - is this a primary key?
        is_nullable: Boolean - can this column be NULL? (default: True)
        is_unique: Boolean - must values be unique?
        default: Default value (Value object) or None
        references: Tuple (table_name, column_name) for foreign keys, or None
    """
    def __init__(self, name, data_type, is_primary=False, is_nullable=True,
                 is_unique=False, default=None, references=None):
        self.name = name
        self.data_type = data_type
        self.is_primary = is_primary
        self.is_nullable = is_nullable
        self.is_unique = is_unique
        self.default = default
        self.references = references  # (table_name, column_name) or None

    def __repr__(self):
        parts = [f"{self.name} {self.data_type}"]
        if self.is_primary:
            parts.append("PRIMARY KEY")
        if not self.is_nullable:
            parts.append("NOT NULL")
        if self.is_unique:
            parts.append("UNIQUE")
        if self.default is not None:
            parts.append(f"DEFAULT {self.default}")
        if self.references:
            parts.append(f"REFERENCES {self.references[0]}({self.references[1]})")
        return " ".join(parts)


class CreateTableStmt(Statement):
    """Represents a CREATE TABLE statement."""
    def __init__(self, table_name, columns):
        self.table_name = table_name
        self.columns = columns  # list of ColumnDef

    def __repr__(self):
        cols_str = ",\n    ".join(str(c) for c in self.columns)
        return f"CREATE TABLE {self.table_name}(\n    {cols_str}\n)"


class AlterTableStmt(Statement):
    """Represents an ALTER TABLE statement."""
    def __init__(self, table_name, action):
        self.table_name = table_name
        self.action = action  # dict: {'type': 'add'|'drop', 'column': ColumnDef|name}

    def __repr__(self):
        if self.action['type'] == 'add':
            return f"ALTER TABLE {self.table_name} ADD {self.action['column']}"
        else:
            return f"ALTER TABLE {self.table_name} DROP {self.action['column']}"


class DropTableStmt(Statement):
    """Represents a DROP TABLE statement."""
    def __init__(self, table_name):
        self.table_name = table_name

    def __repr__(self):
        return f"DROP TABLE {self.table_name}"


# ---------------------------------------------------------------------------
# DML (Data Manipulation Language) Statements
# ---------------------------------------------------------------------------

class ValueNode(ASTNode):
    """Represents a literal value in the AST."""
    def __init__(self, value_str, value_type):
        self.value_str = value_str  # raw string from source
        self.value_type = value_type  # 'int', 'string', 'float', 'boolean', 'null'

    def __repr__(self):
        return f"ValueNode({self.value_str}, {self.value_type})"


class ColumnRef(ASTNode):
    """References a column, possibly with a table prefix (e.g., users.id)."""
    def __init__(self, name, table=None):
        self.name = name
        self.table = table  # table name or None

    def __repr__(self):
        if self.table:
            return f"{self.table}.{self.name}"
        return self.name


class InsertStmt(Statement):
    """Represents an INSERT INTO statement."""
    def __init__(self, table_name, columns, values):
        self.table_name = table_name
        self.columns = columns  # list of column names
        self.values = values    # list of ValueNode (same length as columns)

    def __repr__(self):
        cols = ", ".join(self.columns)
        vals = ", ".join(str(v) for v in self.values)
        return f"INSERT INTO {self.table_name} ({cols}) VALUES ({vals})"


class UpdateStmt(Statement):
    """Represents an UPDATE statement."""
    def __init__(self, table_name, assignments, where_clause=None):
        self.table_name = table_name
        self.assignments = assignments  # list of (column_name, ValueNode) tuples
        self.where_clause = where_clause  # WhereClause or None

    def __repr__(self):
        sets = ", ".join(f"{col} = {val}" for col, val in self.assignments)
        result = f"UPDATE {self.table_name} SET {sets}"
        if self.where_clause:
            result += f" {self.where_clause}"
        return result


class DeleteStmt(Statement):
    """Represents a DELETE statement."""
    def __init__(self, table_name, where_clause=None):
        self.table_name = table_name
        self.where_clause = where_clause  # WhereClause or None

    def __repr__(self):
        result = f"DELETE FROM {self.table_name}"
        if self.where_clause:
            result += f" {self.where_clause}"
        return result


class SelectStmt(Statement):
    """Represents a SELECT statement."""
    def __init__(self, table_name, select_columns, where_clause=None,
                 join=None):
        self.table_name = table_name
        self.select_columns = select_columns  # list of ColumnRef or '*'
        self.where_clause = where_clause  # WhereClause or None
        self.join = join  # JoinClause or None

    def __repr__(self):
        cols = ", ".join(str(c) for c in self.select_columns)
        result = f"SELECT {cols} FROM {self.table_name}"
        if self.join:
            result += f" {self.join}"
        if self.where_clause:
            result += f" {self.where_clause}"
        return result


class JoinClause(ASTNode):
    """Represents a JOIN clause."""
    def __init__(self, join_type, table_name, left_column, right_column):
        self.join_type = join_type  # 'inner'
        self.table_name = table_name
        self.left_column = left_column  # ColumnRef
        self.right_column = right_column  # ColumnRef

    def __repr__(self):
        return (f"JOIN {self.table_name} ON "
                f"{self.left_column} = {self.right_column}")


class WhereClause(ASTNode):
    """Represents a WHERE clause with conditions."""
    def __init__(self, condition):
        self.condition = condition  # ConditionNode

    def __repr__(self):
        return f"WHERE {self.condition}"


# ---------------------------------------------------------------------------
# Expression Nodes
# ---------------------------------------------------------------------------

class ConditionNode(ASTNode):
    """Base class for condition nodes."""
    pass


class BinaryCondition(ConditionNode):
    """
    A binary condition: left OP right
    e.g., age > 25, name = "Alice"
    """
    def __init__(self, left, operator, right):
        self.left = left  # ColumnRef or ValueNode
        self.operator = operator  # '==', '!=', '<', '>', '<=', '>='
        self.right = right  # ColumnRef or ValueNode

    def __repr__(self):
        return f"{self.left} {self.operator} {self.right}"


class LogicalCondition(ConditionNode):
    """
    A logical combination: left AND/OR right
    """
    def __init__(self, left, operator, right):
        self.left = left
        self.operator = operator  # 'AND' or 'OR'
        self.right = right

    def __repr__(self):
        return f"({self.left} {self.operator} {self.right})"


class NotCondition(ConditionNode):
    """A NOT condition."""
    def __init__(self, condition):
        self.condition = condition

    def __repr__(self):
        return f"NOT {self.condition}"


class ParenthesizedCondition(ConditionNode):
    """A parenthesized condition."""
    def __init__(self, condition):
        self.condition = condition

    def __repr__(self):
        return f"({self.condition})"
