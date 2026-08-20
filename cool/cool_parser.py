"""
COOL Parser
Parses a token stream from the lexer into an Abstract Syntax Tree (AST).
"""

from cool_token import *
from cool_ast import *
from cool_exceptions import ParserError


class CoolParser:
    """
    Recursive descent parser for the COOL language.

    Usage:
        lexer = CoolLexer(source_code)
        tokens = lexer.tokenize()
        parser = CoolParser(tokens)
        ast = parser.parse()  # Returns a list of Statement nodes
    """

    def __init__(self, tokens):
        """
        Initialize the parser with a list of tokens.

        Args:
            tokens: List of Token objects from the lexer
        """
        self.tokens = tokens
        self.pos = 0
        self.current = tokens[0] if tokens else None

    # -----------------------------------------------------------------------
    # Core parsing methods
    # -----------------------------------------------------------------------

    def parse(self):
        """
        Parse all statements in the token stream.

        Returns:
            A list of Statement AST nodes
        """
        statements = []
        while self.current.type != TOKEN_EOF:
            # Allow optional semicolons before statements
            self._skip_semicolons()
            if self.current.type == TOKEN_EOF:
                break
            stmt = self._parse_statement()
            if stmt is not None:
                statements.append(stmt)
        return statements

    def _parse_statement(self):
        """
        Parse a single statement by dispatching to the appropriate handler
        based on the first keyword.
        """
        if self.current.type == TOKEN_CREATE:
            return self._parse_create()
        elif self.current.type == TOKEN_ALTER:
            return self._parse_alter()
        elif self.current.type == TOKEN_DROP:
            return self._parse_drop()
        elif self.current.type == TOKEN_INSERT:
            return self._parse_insert()
        elif self.current.type == TOKEN_UPDATE:
            return self._parse_update()
        elif self.current.type == TOKEN_DELETE:
            return self._parse_delete()
        elif self.current.type == TOKEN_SELECT:
            return self._parse_select()
        else:
            raise ParserError(
                f"Unexpected token '{self.current.value}' at line {self.current.line}, "
                f"column {self.current.column}. "
                f"Expected a statement keyword (create, insert, update, delete, select)."
            )

    def _skip_semicolons(self):
        """Skip any semicolons between statements."""
        while self.current.type == TOKEN_SEMICOLON:
            self._advance()

    # -----------------------------------------------------------------------
    # DDL Parsing
    # -----------------------------------------------------------------------

    def _parse_create(self):
        """Parse: CREATE TABLE name ( coldef, coldef, ... )"""
        self._expect(TOKEN_CREATE)
        self._expect(TOKEN_TABLE)
        table_name = self._expect_identifier()
        columns = self._parse_column_definitions()
        return CreateTableStmt(table_name, columns)

    def _parse_alter(self):
        """Parse: ALTER TABLE name ADD | DROP"""
        self._expect(TOKEN_ALTER)
        self._expect(TOKEN_TABLE)
        table_name = self._expect_identifier()

        action_keyword = self.current.type
        if action_keyword == TOKEN_IDENTIFIER and self.current.value.lower() == 'add':
            self._advance()
            # Parse a single column definition
            col_name = self._expect_identifier()
            data_type = self._parse_type()
            col_def = ColumnDef(col_name, data_type)
            # Parse any constraints
            self._parse_constraints(col_def)
            return AlterTableStmt(table_name, {'type': 'add', 'column': col_def})
        elif action_keyword == TOKEN_IDENTIFIER and self.current.value.lower() == 'drop':
            self._advance()
            col_name = self._expect_identifier()
            return AlterTableStmt(table_name, {'type': 'drop', 'column': col_name})
        else:
            raise ParserError(
                f"Expected ADD or DROP after ALTER TABLE, got '{self.current.value}'"
            )

    def _parse_drop(self):
        """Parse: DROP TABLE name"""
        self._expect(TOKEN_DROP)
        self._expect(TOKEN_TABLE)
        table_name = self._expect_identifier()
        return DropTableStmt(table_name)

    def _parse_column_definitions(self):
        """
        Parse column definitions in either parentheses or braces.
        ( col: type [constraints], ... )
        { col: type [constraints], ... }
        """
        columns = []

        # Accept either ( ) or { }
        if self.current.type == TOKEN_LPAREN:
            self._expect(TOKEN_LPAREN)
            close_token = TOKEN_RPAREN
        elif self.current.type == TOKEN_LBRACE:
            self._expect(TOKEN_LBRACE)
            close_token = TOKEN_RBRACE
        else:
            raise ParserError(
                f"Expected '(' or '{{' to start column definitions, got '{self.current.value}'"
            )

        # Allow empty table (edge case)
        if self.current.type == close_token:
            self._expect(close_token)
            return columns

        while True:
            col_name = self._expect_identifier()
            self._expect_value(':')  # expect colon separator
            data_type = self._parse_type()
            col_def = ColumnDef(col_name, data_type)

            # Parse constraints
            self._parse_constraints(col_def)

            columns.append(col_def)

            if self.current.type == TOKEN_COMMA:
                self._advance()
                # Allow trailing comma
                if self.current.type == close_token:
                    break
            elif self.current.type == close_token:
                break
            else:
                raise ParserError(
                    f"Expected ',' or '{close_token}' in column definitions, "
                    f"got '{self.current.value}'"
                )

        self._expect(close_token)
        return columns

    def _parse_constraints(self, col_def):
        """Parse column-level constraints: PRIMARY KEY, NOT NULL, UNIQUE, REFERENCES, DEFAULT."""
        while True:
            if self.current.type == TOKEN_PRIMARY:
                self._advance()
                self._expect(TOKEN_KEY)
                col_def.is_primary = True
                col_def.is_nullable = False
            elif self.current.type == TOKEN_NULL:
                self._advance()
                # Check for "NOT NULL"
                if self.current.type == TOKEN_NOT:
                    self._advance()
                    self._expect(TOKEN_NULL)
                    col_def.is_nullable = False
            elif self.current.type == TOKEN_UNIQUE:
                self._advance()
                col_def.is_unique = True
            elif self.current.type == TOKEN_REFERENCES:
                self._advance()
                ref_table = self._expect_identifier()
                self._expect(TOKEN_LPAREN)
                ref_col = self._expect_identifier()
                self._expect(TOKEN_RPAREN)
                col_def.references = (ref_table, ref_col)
            elif self.current.type == TOKEN_DEFAULT:
                self._advance()
                default_val = self._parse_value()
                col_def.default = default_val
            else:
                break

    def _parse_type(self):
        """Parse a type keyword: INT, STRING, FLOAT, BOOLEAN."""
        if self.current.type == TOKEN_INT:
            self._advance()
            return 'int'
        elif self.current.type == TOKEN_STRING:
            self._advance()
            return 'string'
        elif self.current.type == TOKEN_FLOAT:
            self._advance()
            return 'float'
        elif self.current.type == TOKEN_BOOLEAN:
            self._advance()
            return 'boolean'
        else:
            raise ParserError(
                f"Expected a type (int, string, float, boolean), got '{self.current.value}'"
            )

    # -----------------------------------------------------------------------
    # DML Parsing
    # -----------------------------------------------------------------------

    def _parse_insert(self):
        """
        Parse: INSERT INTO table (col1, col2) VALUES (val1, val2)
           or INSERT INTO table VALUES (val1, val2)
        """
        self._expect(TOKEN_INSERT)
        self._expect(TOKEN_INTO)
        table_name = self._expect_identifier()

        columns = []
        # Check if columns are specified
        if self.current.type == TOKEN_LPAREN:
            self._expect(TOKEN_LPAREN)
            while True:
                columns.append(self._expect_identifier())
                if self.current.type == TOKEN_COMMA:
                    self._advance()
                elif self.current.type == TOKEN_RPAREN:
                    self._advance()
                    break
                else:
                    raise ParserError(
                        f"Expected ',' or ')' in column list, got '{self.current.value}'"
                    )

        self._expect(TOKEN_VALUES)
        self._expect(TOKEN_LPAREN)

        values = []
        while True:
            values.append(self._parse_value())
            if self.current.type == TOKEN_COMMA:
                self._advance()
            elif self.current.type == TOKEN_RPAREN:
                self._advance()
                break
            else:
                raise ParserError(
                    f"Expected ',' or ')' in VALUES, got '{self.current.value}'"
                )

        return InsertStmt(table_name, columns, values)

    def _parse_update(self):
        """Parse: UPDATE table SET col = val, ... [WHERE condition]"""
        self._expect(TOKEN_UPDATE)
        table_name = self._expect_identifier()
        self._expect(TOKEN_SET)

        assignments = []
        while True:
            col_name = self._expect_identifier()
            self._expect(TOKEN_EQ)
            value = self._parse_value()
            assignments.append((col_name, value))

            if self.current.type == TOKEN_COMMA:
                self._advance()
            else:
                break

        where_clause = None
        if self.current.type == TOKEN_WHERE:
            where_clause = self._parse_where()

        return UpdateStmt(table_name, assignments, where_clause)

    def _parse_delete(self):
        """Parse: DELETE FROM table [WHERE condition]"""
        self._expect(TOKEN_DELETE)
        self._expect(TOKEN_FROM)
        table_name = self._expect_identifier()

        where_clause = None
        if self.current.type == TOKEN_WHERE:
            where_clause = self._parse_where()

        return DeleteStmt(table_name, where_clause)

    def _parse_select(self):
        """
        Parse: SELECT cols FROM table [JOIN table ON cond] [WHERE condition]
        Also supports the shorthand: SELECT * FROM table
        """
        self._expect(TOKEN_SELECT)

        # Parse select columns
        select_columns = []
        if self.current.type == TOKEN_STAR:
            select_columns.append(ColumnRef('*'))
            self._advance()
        else:
            while True:
                # Parse column reference: name or table.name
                if self.current.type == TOKEN_IDENTIFIER:
                    col_name = self._advance_identifier()
                else:
                    raise ParserError(
                        f"Expected column name, got '{self.current.value}'"
                    )

                if self.current.type == TOKEN_DOT:
                    self._advance()
                    table_name = col_name
                    col_name = self._expect_identifier()
                    ref = ColumnRef(col_name, table_name)
                else:
                    ref = ColumnRef(col_name)

                select_columns.append(ref)

                if self.current.type == TOKEN_COMMA:
                    self._advance()
                else:
                    break

        self._expect(TOKEN_FROM)
        table_name = self._expect_identifier()

        # Parse optional JOIN
        join = None
        if self.current.type == TOKEN_JOIN:
            join = self._parse_join()

        # Parse optional WHERE
        where_clause = None
        if self.current.type == TOKEN_WHERE:
            where_clause = self._parse_where()

        return SelectStmt(table_name, select_columns, where_clause, join)

    def _parse_join(self):
        """Parse: JOIN table [INNER] ON left = right"""
        self._expect(TOKEN_JOIN)

        # Support optional INNER keyword
        join_type = 'inner'
        if self.current.type == TOKEN_IDENTIFIER and self.current.value.lower() == 'inner':
            self._advance()

        table_name = self._expect_identifier()
        self._expect(TOKEN_ON)

        left = self._parse_column_ref()
        self._expect_operator()  # expect '='
        right = self._parse_column_ref()

        return JoinClause(join_type, table_name, left, right)

    def _parse_where(self):
        """Parse: WHERE condition"""
        self._expect(TOKEN_WHERE)
        condition = self._parse_condition()
        return WhereClause(condition)

    def _parse_condition(self):
        """
        Parse a condition expression.
        Handles AND, OR, NOT, parentheses, and binary comparisons.
        """
        return self._parse_or_condition()

    def _parse_or_condition(self):
        """Parse OR-separated conditions."""
        left = self._parse_and_condition()
        while self.current.type == TOKEN_OR:
            self._advance()
            right = self._parse_and_condition()
            left = LogicalCondition(left, 'OR', right)
        return left

    def _parse_and_condition(self):
        """Parse AND-separated conditions."""
        left = self._parse_not_condition()
        while self.current.type == TOKEN_AND:
            self._advance()
            right = self._parse_not_condition()
            left = LogicalCondition(left, 'AND', right)
        return left

    def _parse_not_condition(self):
        """Parse NOT conditions."""
        if self.current.type == TOKEN_NOT:
            self._advance()
            return NotCondition(self._parse_not_condition())
        return self._parse_primary_condition()

    def _parse_primary_condition(self):
        """Parse a primary condition: comparison or parenthesized expression."""
        if self.current.type == TOKEN_LPAREN:
            self._advance()
            condition = self._parse_condition()
            self._expect(TOKEN_RPAREN)
            return ParenthesizedCondition(condition)
        return self._parse_comparison()

    def _parse_comparison(self):
        """Parse a binary comparison: left OP right."""
        left = self._parse_operand()
        operator = self._expect_operator()
        right = self._parse_operand()
        return BinaryCondition(left, operator, right)

    def _parse_operand(self):
        """Parse an operand: either a column reference or a value."""
        if self._is_value_start():
            return self._parse_value()
        return self._parse_column_ref()

    def _parse_column_ref(self):
        """Parse a column reference: name or table.name."""
        name = self._expect_identifier()
        if self.current.type == TOKEN_DOT:
            self._advance()
            table = name
            col = self._expect_identifier()
            return ColumnRef(col, table)
        return ColumnRef(name)

    def _parse_value(self):
        """Parse a literal value (number, string, boolean, null)."""
        token_type = self.current.type
        if token_type == TOKEN_NUMBER_INT:
            val = ValueNode(self.current.value, 'int')
            self._advance()
            return val
        elif token_type == TOKEN_NUMBER_FLOAT:
            val = ValueNode(self.current.value, 'float')
            self._advance()
            return val
        elif token_type == TOKEN_STRING_LITERAL:
            # Strip quotes
            raw = self.current.value
            if raw.startswith('"') and raw.endswith('"'):
                value = raw[1:-1]
            elif raw.startswith("'") and raw.endswith("'"):
                value = raw[1:-1]
            else:
                value = raw
            val = ValueNode(value, 'string')
            self._advance()
            return val
        elif token_type == TOKEN_BOOLEAN_LITERAL:
            val = ValueNode(self.current.value, 'boolean')
            self._advance()
            return val
        elif token_type == TOKEN_NULL_LITERAL:
            val = ValueNode('null', 'null')
            self._advance()
            return val
        else:
            raise ParserError(
                f"Expected a value, got '{self.current.value}' at line {self.current.line}"
            )

    def _parse_type(self):
        """Parse a type keyword: INT, STRING, FLOAT, BOOLEAN."""
        if self.current.type == TOKEN_INT:
            self._advance()
            return 'int'
        elif self.current.type == TOKEN_STRING:
            self._advance()
            return 'string'
        elif self.current.type == TOKEN_FLOAT:
            self._advance()
            return 'float'
        elif self.current.type == TOKEN_BOOLEAN:
            self._advance()
            return 'boolean'
        else:
            raise ParserError(
                f"Expected a type (int, string, float, boolean), got '{self.current.value}'"
            )

    # -----------------------------------------------------------------------
    # Helper methods
    # -----------------------------------------------------------------------

    def _is_value_start(self):
        """Check if the current token starts a value literal."""
        return self.current.type in (
            TOKEN_NUMBER_INT, TOKEN_NUMBER_FLOAT,
            TOKEN_STRING_LITERAL, TOKEN_BOOLEAN_LITERAL, TOKEN_NULL_LITERAL
        )

    def _expect_token_type(self, token_type):
        """Expect a specific token type, advance if matched, error otherwise."""
        if self.current.type == token_type:
            self._advance()
            return True
        else:
            raise ParserError(
                f"Expected {token_type} but got {self.current.type} "
                f"('{self.current.value}'') at line {self.current.line}, "
                f"column {self.current.column}"
            )

    def _expect(self, token_type):
        """Expect a specific token type."""
        return self._expect_token_type(token_type)

    def _expect_identifier(self):
        """Expect an identifier token and return its value."""
        if self.current.type != TOKEN_IDENTIFIER:
            raise ParserError(
                f"Expected an identifier, got '{self.current.value}' "
                f"({self.current.type}) at line {self.current.line}"
            )
        value = self.current.value
        self._advance()
        return value

    def _advance_identifier(self):
        """Consume an identifier token and return its value."""
        if self.current.type != TOKEN_IDENTIFIER:
            raise ParserError(
                f"Expected an identifier, got '{self.current.value}' at line {self.current.line}"
            )
        value = self.current.value
        self._advance()
        return value

    def _expect_value(self, value):
        """Expect a specific literal value."""
        if self.current.value == value:
            self._advance()
            return True
        else:
            raise ParserError(
                f"Expected '{value}' but got '{self.current.value}' "
                f"at line {self.current.line}, column {self.current.column}"
            )

    def _expect_operator(self):
        """
        Expect a comparison operator and return its COOL representation.
        Maps token types to operator strings: '==', '!=', '<', '>', '<=', '>='
        """
        op_map = {
            TOKEN_EQ: '==',
            TOKEN_NEQ: '!=',
            TOKEN_LT: '<',
            TOKEN_GT: '>',
            TOKEN_LE: '<=',
            TOKEN_GE: '>=',
        }
        if self.current.type in op_map:
            op = op_map[self.current.type]
            self._advance()
            return op
        else:
            raise ParserError(
                f"Expected a comparison operator (=, !=, <, >, <=, >=) but got "
                f"'{self.current.value}' at line {self.current.line}"
            )

    def _advance(self):
        """Move to the next token."""
        self.pos += 1
        if self.pos < len(self.tokens):
            self.current = self.tokens[self.pos]
        else:
            self.current = self.tokens[-1]  # Stay at EOF
