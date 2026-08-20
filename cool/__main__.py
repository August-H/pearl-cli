"""
COOL Language - Main Entry Point
Run with: python -m cool [command]

This file allows the COOL package to be run as a module.
Example: python -m cool repl
         python -m cool run script.cool
         python cool exec "create table users { id: int primary key };"
"""

from cool_cli import CoolCLI


def main():
    """Main entry point for the COOL CLI."""
    cli = CoolCLI()
    cli.run()


if __name__ == "__main__":
    main()
