"""
COOL Interpreter
Executes AST statements against a Database engine.
"""

from cool_ast import *
from cool_database import Database, Table, Column
from cool_value import Value
from cool_exceptions import ExecutionError, DatabaseError, TableError, ColumnError, DataError


class CoolInterpreter:
    """
    Executes COOL AST statements against a Database.

    Usage:
        db = Database()
        interp = CoolInterpreter(db)
        results = interp.execute(statements)
    """

    def __init__(self, database=None):
        """
        Initialize the interpreter with a database.

        Args:
            database: A Database object (creates a new one if None)
        """
        self.db = database if database is not None else Database()
        self.last_results = []  # Results from the last SELECT

    def execute(self, statements):
        """
        Execute a list of statements.

        Args:
            statements: List of AST Statement nodes

        Returns:
            List of result sets (from SELECT statements)

        Raises:
            ExecutionError: If any statement fails
        """
        results = []
        for stmt in statements:
            result = self._execute_statement(stmt)
            if result is not None:
                self.last_results = result
                results.append(result)
        return results

    def execute_single(self, statement):
        """
        Execute a single statement.

        Returns:
            Result (list of rows) if SELECT, None otherwise
        """
        result = self._execute_statement(statement)
        if result is not None:
            self.last_results = result
        return result

    def _execute_statement(self, stmt):
        """Dispatch a single statement to the appropriate handler."""
        if isinstance(stmt, CreateTableStmt):
            self._execute_create_table(stmt)
            return None
        elif isinstance(stmt, AlterTableStmt):
            self._execute_alter_table(stmt)
            return None
        elif isinstance(stmt, DropTableStmt):
            self._execute_drop_table(stmt)
            return None
        elif isinstance(stmt, InsertStmt):
            self._execute_insert(stmt)
            return None
        elif isinstance(stmt, UpdateStmt):
            return self._execute_update(stmt)
        elif isinstance(stmt, DeleteStmt):
            return self._execute_delete(stmt)
        elif isinstance(stmt, SelectStmt):
            return self._execute_select(stmt)
        else:
            raise ExecutionError(f"Unknown statement type: {type(stmt).__name__}")

    # -----------------------------------------------------------------------
    # DDL Execution
    # -----------------------------------------------------------------------

    def _execute_create_table(self, stmt):
        """Execute a CREATE TABLE statement."""
        # Convert ColumnDef objects from AST to ColumnDef (reuse db's logic)
        column_defs = []
        for col_ast in stmt.columns:
            # The col_ast is already a ColumnDef from parser
            column_defs.append(col_ast)

        self.db.create_table(stmt.table_name, column_defs)

    def _execute_alter_table(self, stmt):
        """Execute an ALTER TABLE statement."""
        if stmt.action['type'] == 'add':
            self.db.alter_table_add_column(stmt.table_name, stmt.action['column'])
            print(f"Column '{stmt.action['column'].name}' added to table '{stmt.table_name}'")
        elif stmt.action['type'] == 'drop':
            self.db.alter_table_drop_column(stmt.table_name, stmt.action['column'])
            print(f"Column '{stmt.action['column']}' dropped from table '{stmt.table_name}'")

    def _execute_drop_table(self, stmt):
        """Execute a DROP TABLE statement."""
        self.db.drop_table(stmt.table_name)

    # -----------------------------------------------------------------------
    # DML Execution
    # -----------------------------------------------------------------------

    def _execute_insert(self, stmt):
        """Execute an INSERT statement."""
        table = self.db.get_table(stmt.table_name)

        # If no columns specified, insert in column order
        if not stmt.columns:
            stmt.columns = list(table.column_order)

        # Convert ValueNode to Value objects
        values = [self._value_node_to_value(vn) for vn in stmt.values]

        table.insert(stmt.columns, values)

    def _execute_update(self, stmt):
        """Execute an UPDATE statement."""
        table = self.db.get_table(stmt.table_name)

        # Build assignments dict
        assignments = {}
        for col_name, value_node in stmt.assignments:
            value = self._value_node_to_value(value_node)
            # Validate type
            col = table.get_column(col_name)
            col.validate_value(value)
            assignments[col_name] = value

        # Build WHERE clause
        where_func = None
        if stmt.where_clause:
            where_func = self._build_where_function(stmt.where_clause, table)

        count = table.update(assignments, where_func)
        return count

    def _execute_delete(self, stmt):
        """Execute a DELETE statement."""
        table = self.db.get_table(stmt.table_name)

        where_func = None
        if stmt.where_clause:
            where_func = self._build_where_function(stmt.where_clause, table)

        count = table.delete(where_func)
        return count

    def _execute_select(self, stmt):
        """Execute a SELECT statement."""
        table = self.db.get_table(stmt.table_name)

        # Build WHERE clause
        where_func = None
        if stmt.where_clause:
            where_func = self._build_where_function(stmt.where_clause, table)

        # Determine which columns to select
        all_columns = stmt.select_columns[0] if stmt.select_columns else ColumnRef('*')
        if all_columns.name == '*':
            column_names = None  # All columns
        else:
            column_names = [c.name for c in stmt.select_columns]

        # Handle JOIN
        if stmt.join:
            results = self._execute_join(stmt, table, where_func, column_names)
        else:
            rows = table.select_all(column_names, where_func)
            results = [row for row in rows]

        # Store column metadata for display
        if column_names is None:
            display_columns = table.column_order
        else:
            display_columns = column_names

        return {
            'columns': display_columns,
            'rows': results,
            'table': table.name,
        }

    def _execute_join(self, stmt, left_table, where_func, column_names):
        """Execute a SELECT with a JOIN."""
        right_table = self.db.get_table(stmt.join.table_name)

        left_col = stmt.join.left_column.name
        right_col = stmt.join.right_column.name

        results = []

        for left_row in left_table.rows:
            left_val = left_row.get_value(left_col)
            if left_val is None or left_val.is_null():
                continue

            for right_row in right_table.rows:
                right_val = right_row.get_value(right_col)
                if right_val is None or right_val.is_null():
                    continue

                if left_val == right_val:
                    # Check WHERE clause against joined row
                    if where_func is not None:
                        # Merge rows for where clause evaluation
                        merged = Row()
                        merged._values.update(left_row._values)
                        merged._values.update(right_row._values)
                        if not where_func(merged):
                            continue

                    # Build joined row
                    joined = Row()
                    for col_name in left_table.column_order:
                        val = left_row.get_value(col_name)
                        if val:
                            joined.set_value(col_name, val.clone())
                    for col_name in right_table.column_order:
                        val = right_row.get_value(col_name)
                        if val:
                            joined.set_value(col_name, val.clone())
                    results.append(joined)

        return results

    # -----------------------------------------------------------------------
    # Helper methods
    # -----------------------------------------------------------------------

    def _value_node_to_value(self, value_node):
        """Convert a ValueNode AST node to a Value object."""
        if value_node.value_type == 'int':
            return Value(int(value_node.value_str), 'int')
        elif value_node.value_type == 'float':
            return Value(float(value_node.value_str), 'float')
        elif value_node.value_type == 'string':
            return Value(value_node.value_str, 'string')
        elif value_node.value_type == 'boolean':
            return Value(value_node.value_str == 'true', 'boolean')
        elif value_node.value_type == 'null':
            return Value(None, 'null')
        else:
            raise DataError(f"Unknown value type: {value_node.value_type}")

    def _build_where_function(self, where_clause, table):
        """
        Build a where clause evaluation function from a WhereClause AST node.
        Returns a function that takes a Row and returns True/False.
        """
        # The where clause's condition references columns in the table.
        # We need to also check if it's a joined table context.
        # For simplicity, we'll resolve columns from the table first,
        # then from any available context.

        def evaluate(row):
            return self._evaluate_condition(where_clause.condition, row, table)

        return evaluate

    def _evaluate_condition(self, condition, row, table):
        """Evaluate a condition node against a row."""
        if isinstance(condition, BinaryCondition):
            return self._evaluate_binary_condition(condition, row, table)
        elif isinstance(condition, LogicalCondition):
            left = self._evaluate_condition(condition.left, row, table)
            right = self._evaluate_condition(condition.right, row, table)
            if condition.operator == 'AND':
                return left and right
            elif condition.operator == 'OR':
                return left or right
            else:
                raise ExecutionError(f"Unknown logical operator: {condition.operator}")
        elif isinstance(condition, NotCondition):
            return not self._evaluate_condition(condition.condition, row, table)
        elif isinstance(condition, ParenthesizedCondition):
            return self._evaluate_condition(condition.condition, row, table)
        else:
            raise ExecutionError(
                f"Unknown condition type: {type(condition).__name__}"
            )

    def _evaluate_binary_condition(self, condition, row, table):
        """Evaluate a binary comparison condition."""
        left_val = self._resolve_operand(condition.left, row, table)
        right_val = self._resolve_operand(condition.right, row, table)

        # Handle null comparisons
        if left_val.is_null() or right_val.is_null():
            if condition.operator == '==':
                return left_val.is_null() and right_val.is_null()
            elif condition.operator == '!=':
                return not (left_val.is_null() and right_val.is_null())
            else:
                # Comparisons with NULL are false
                return False

        if condition.operator == '==':
            return left_val == right_val
        elif condition.operator == '!=':
            # Need to handle Value comparison
            if left_val.type == right_val.type:
                return left_val.value != right_val.value
            try:
                return left_val.value != right_val.value
            except:
                return False
        elif condition.operator == '<':
            return left_val < right_val
        elif condition.operator == '>':
            return left_val > right_val
        elif condition.operator == '<=':
            return left_val <= right_val
        elif condition.operator == '>=':
            return left_val >= right_val
        else:
            raise ExecutionError(f"Unknown comparison operator: {condition.operator}")

    def _resolve_operand(self, operand, row, table):
        """Resolve an operand (ColumnRef or ValueNode) to a Value from a row."""
        if isinstance(operand, ColumnRef):
            col_name = operand.name
            if col_name not in row._values:
                # Column not in this row (could happen in joins)
                # Try to get from table
                if col_name in table.columns:
                    # Return null for missing values
                    return Value(None, 'null')
                raise ColumnError(
                    f"Column '{col_name}' not found in row context"
                )
            val = row._values[col_name]
            if val is None:
                return Value(None, 'null')
            return val
        elif isinstance(operand, ValueNode):
            return self._value_node_to_value(operand)
        else:
            raise ExecutionError(
                f"Unknown operand type: {type(operand).__name__}"
            )
