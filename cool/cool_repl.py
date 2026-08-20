"""
COOL REPL (Interactive Shell)
Provides an interactive session for executing COOL commands.
"""

import sys
from cool_lexer import CoolLexer
from cool_parser import CoolParser
from cool_interpreter import CoolInterpreter
from cool_database import Database
from cool_exceptions import CoolError


class CoolREPL:
    """
    Interactive Read-Eval-Print Loop for the COOL language.

    Usage:
        repl = CoolREPL()
        repl.run()
    """

    def __init__(self, database=None, prompt="cool> "):
        """
        Initialize the REPL.

        Args:
            database: A Database object (creates new one if None)
            prompt: The prompt string
        """
        self.db = database if database is not None else Database("repl_db")
        self.interpreter = CoolInterpreter(self.db)
        self.prompt = prompt
        self._buffer = ""
        self._statement_terminated = True

    def run(self):
        """Start the REPL loop."""
        print(f"COOL Database Shell v1.0.0")
        print(f"Connected to database '{self.db.name}'.")
        print(f"Type '.help' for commands, '.exit' or Ctrl+D to quit.\n")

        while True:
            try:
                if self._statement_terminated:
                    user_input = input(self.prompt)
                else:
                    user_input = input("... ")

                if user_input is None:
                    # Ctrl+D
                    print()
                    break

                stripped = user_input.strip()

                if not stripped:
                    continue

                # Handle special commands
                if stripped.startswith('.'):
                    if self._handle_command(stripped):
                        continue
                    else:
                        self._buffer += user_input + "\n"
                        continue

                # Normal COOL statement
                self._buffer += user_input + "\n"

                # Check if statement is complete (ends with semicolon)
                if stripped.endswith(';'):
                    self._statement_terminated = True
                    code = self._buffer
                    self._buffer = ""
                    self._execute_code(code)
                else:
                    self._statement_terminated = False

            except EOFError:
                print()
                break
            except KeyboardInterrupt:
                print("\nInterrupted.")
                self._buffer = ""
                self._statement_terminated = True
                continue

        print("Goodbye!")

    def _handle_command(self, command):
        """
        Handle dot-commands. Returns True if command was handled.
        """
        if command == '.exit':
            print("Goodbye!")
            sys.exit(0)
        elif command == '.help':
            self._show_help()
        elif command == '.tables':
            self._show_tables()
        elif command == '.schema':
            self._show_schema()
        elif command.startswith('.schema '):
            table_name = command.split(None, 1)[1].strip()
            self._show_schema(table_name)
        elif command.startswith('.save'):
            parts = command.split(None, 1)
            if len(parts) > 1:
                filepath = parts[1].strip()
            else:
                filepath = "cool_db.json"
            self.db.save_to_file(filepath)
            print(f"Database saved to '{filepath}'")
        elif command.startswith('.load'):
            parts = command.split(None, 1)
            if len(parts) > 1:
                filepath = parts[1].strip()
            else:
                filepath = "cool_db.json"
            self.db = Database.load_from_file(filepath)
            self.interpreter = CoolInterpreter(self.db)
            print(f"Database loaded from '{filepath}'")
        else:
            print(f"Unknown command: {command}")
        return True

    def _show_help(self):
        """Show help text."""
        print("""
COOL Database Shell Commands:
  .exit              - Exit the shell
  .help              - Show this help message
  .tables            - List all tables
  .schema [table]    - Show schema (optionally for a specific table)
  .save [file]       - Save database to a JSON file (default: cool_db.json)
  .load [file]       - Load database from a JSON file (default: cool_db.json)

COOL Language Examples:
  Table creation:
    create table users {
      id: int primary key,
      name: string not null,
      email: string unique,
      age: int
    }

  Insert:
    insert into users (id, name, email, age) values (1, "Alice", "alice@ex.com", 30);

  Query:
    select * from users where age > 25;

  Update:
    update users set age = 31 where id = 1;

  Delete:
    delete from users where id = 1;

  Relationships:
    create table orders {
      id: int primary key,
      user_id: int references users(id),
      amount: float
    }

  Join:
    select * from users join orders on users.id = orders.user_id;
""")

    def _show_tables(self):
        """Show all tables in the database."""
        tables = self.db.list_tables()
        if not tables:
            print("(no tables)")
            return
        print("Tables:")
        for t in tables:
            table = self.db.get_table(t)
            print(f"  {t} ({table.get_row_count()} rows)")

    def _show_schema(self, table_name=None):
        """Show schema for all or a specific table."""
        if table_name:
            if table_name not in self.db.tables:
                print(f"Table '{table_name}' does not exist")
                return
            cols = [self.db.tables[table_name].columns[cn]
                    for cn in self.db.tables[table_name].column_order]
            print(f"Table '{table_name}':")
            for col in cols:
                print(f"  {col}")
        else:
            for tname in self.db.list_tables():
                table = self.db.get_table(tname)
                print(f"\nTable '{tname}':")
                for cn in table.column_order:
                    col = table.columns[cn]
                    print(f"  {col}")

    def _execute_code(self, code):
        """Execute a code string through the full pipeline."""
        try:
            lexer = CoolLexer(code)
            tokens = lexer.tokenize()
            parser = CoolParser(tokens)
            statements = parser.parse()
            results = self.interpreter.execute(statements)
            self._print_results(results)
        except CoolError as e:
            print(f"Error: {e}")
        except Exception as e:
            print(f"Unexpected error: {e}")

    def _print_results(self, results):
        """Print the results of executed statements."""
        if not results:
            # Print confirmation for non-SELECT statements
            return

        for i, result in enumerate(results):
            if isinstance(result, dict):
                columns = result['columns']
                rows = result['rows']

                if not rows:
                    print(f"(no rows returned)")
                    continue

                # Build table header
                headers = columns
                header_line = " | ".join(f"{h:<15}" for h in headers)
                print(header_line)
                print("-" * len(header_line))

                for row in rows:
                    values = []
                    for col_name in columns:
                        val = row.get_value(col_name)
                        if val is None:
                            values.append(f"{'NULL':<15}")
                        else:
                            values.append(f"{str(val):<15}")
                    print(" | ".join(values))

                print(f"\n({len(rows)} row{'s' if len(rows) != 1 else ''} returned)\n")
            else:
                # Numeric result (from UPDATE/DELETE)
                print(f"{result} row(s) affected\n")
