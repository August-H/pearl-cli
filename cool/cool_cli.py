"""
COOL CLI
Command-line interface for executing COOL scripts and managing the database.
"""

import sys
import os

from cool_lexer import CoolLexer
from cool_parser import CoolParser
from cool_interpreter import CoolInterpreter
from cool_database import Database
from cool_repl import CoolREPL
from cool_exceptions import CoolError
from cool_value import Value


class CoolCLI:
    """
    Command-line interface for the COOL database language.

    Usage:
        cli = CoolCLI()
        cli.run()
        # or
        cli.run_file("script.cool")
    """

    def __init__(self):
        self.db = Database("main")

    def run(self, args=None):
        """
        Main entry point for the CLI.

        Args:
            args: Command-line arguments (excluding program name)
        """
        if args is None:
            args = sys.argv[1:]

        if not args:
            self._show_help()
            return

        command = args[0]

        if command == "repl":
            self._run_repl()
        elif command == "run":
            if len(args) < 2:
                print("Error: 'run' requires a file path argument.")
                print("Usage: cool run <file.cool>")
                return
            self._run_file(args[1])
        elif command == "exec":
            if len(args) < 2:
                print("Error: 'exec' requires a code string argument.")
                print("Usage: cool exec \"<COOL code>\"")
                return
            self._run_code(args[1])
        elif command == "init":
            self._init_database()
        elif command == "help":
            self._show_help()
        elif command in ("-h", "--help", "help"):
            self._show_help()
        else:
            # If it's a file ending in .cool, run it directly
            if command.endswith('.cool'):
                self._run_file(command)
            else:
                print(f"Unknown command: {command}")
                print()
                self._show_help()

    def _run_file(self, filepath):
        """Run a .cool script file."""
        if not os.path.exists(filepath):
            print(f"Error: File not found: {filepath}")
            return

        with open(filepath, 'r') as f:
            code = f.read()

        print(f"Executing COOL script: {filepath}")
        print("=" * 50)

        try:
            self._execute_code(code)
            print("=" * 50)
            print("Script executed successfully.")
        except CoolError as e:
            print("=" * 50)
            print(f"Error: {e}")
            sys.exit(1)
        except Exception as e:
            print("=" * 50)
            print(f"Unexpected error: {e}")
            import traceback
            traceback.print_exc()
            sys.exit(1)

    def _run_code(self, code):
        """Execute a COOL code string."""
        try:
            self._execute_code(code)
        except CoolError as e:
            print(f"Error: {e}")
        except Exception as e:
            print(f"Unexpected error: {e}")

    def _execute_code(self, code):
        """Execute COOL code through the full pipeline."""
        lexer = CoolLexer(code)
        tokens = lexer.tokenize()
        parser = CoolParser(tokens)
        statements = parser.parse()
        interpreter = CoolInterpreter(self.db)
        results = interpreter.execute(statements)
        self._print_results(results)

    def _run_repl(self):
        """Start the interactive REPL."""
        repl = CoolREPL(self.db)
        repl.run()

    def _init_database(self):
        """Initialize/create the database."""
        self.db = Database("main")
        print("Database 'main' initialized.")

    def _show_help(self):
        """Show CLI help."""
        print("""
COOL Database Management System v1.0.0

COOL is a simple, intuitive relational database management language.

Usage:
    cool run <file.cool>     - Execute a COOL script file
    cool repl                - Start the interactive REPL
    cool exec "<code>"       - Execute COOL code from command line
    cool init                - Initialize a new database
    cool help                - Show this help message

Examples:
    cool run example.cool
    cool repl
    cool exec "create table users { id: int primary key, name: string };"

For more information, see the documentation at README.md
""")

    def _print_results(self, results):
        """Print query results."""
        if not results:
            return

        for i, result in enumerate(results):
            if isinstance(result, dict):
                columns = result['columns']
                rows = result['rows']

                if not rows:
                    print("(no rows returned)")
                    continue

                # Print header
                header_line = " | ".join(f"{h:<20}" for h in columns)
                print(header_line)
                print("-" * len(header_line))

                # Print rows
                for row in rows:
                    values = []
                    for col_name in columns:
                        val = row.get_value(col_name)
                        if val is None:
                            values.append(f"{'NULL':<20}")
                        else:
                            values.append(f"{str(val):<20}")
                    print(" | ".join(values))

                print(f"\n({len(rows)} row{'s' if len(rows) != 1 else ''} returned)\n")
            else:
                print(f"{result} row(s) affected")
