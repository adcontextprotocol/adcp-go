#!/usr/bin/env python3
"""Unit tests for generate.py — run with: python3 -m unittest test_generate"""

import sys
import unittest
from pathlib import Path

# Allow direct import from the same directory.
sys.path.insert(0, str(Path(__file__).parent))
import generate as gen


class TestDeprecatedField(unittest.TestCase):
    """Tests that deprecated: true in a JSON Schema property produces a
    preceding // Deprecated: doc comment in the emitted Go struct field.

    Go's documentation comment convention requires the // Deprecated: paragraph
    to immediately precede the symbol declaration (see go.dev/doc/comment).
    A trailing inline comment (e.g. `Field string \`json:"f"\` // Deprecated:`)
    is not recognized by gopls or go doc and will not surface the deprecation
    to IDE users or go documentation consumers."""

    def _struct_lines(self, schema):
        """Return the struct body lines (inside the braces) as a list."""
        output = gen.schema_to_struct("TestStruct", schema)
        lines = output.splitlines()
        # Strip the type header and closing brace.
        return [l for l in lines if l.startswith('\t')]

    def test_deprecated_field_emits_preceding_comment(self):
        schema = {
            "type": "object",
            "properties": {
                "status": {
                    "type": "string",
                    "deprecated": True,
                    "description": "Use new_status instead.",
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 2,
            "Deprecated field should produce exactly 2 lines: comment + field")
        self.assertEqual(lines[0], "\t// Deprecated: Use new_status instead.")
        self.assertTrue(lines[1].startswith("\tStatus string"),
            f"Field line should start with field declaration, got: {lines[1]!r}")
        # The field line must NOT carry a trailing inline comment.
        self.assertNotIn("//", lines[1],
            "Trailing inline comment suppressed for deprecated fields")

    def test_deprecated_field_without_description_uses_fallback(self):
        schema = {
            "type": "object",
            "properties": {
                "old_field": {
                    "type": "string",
                    "deprecated": True,
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(lines[0], "\t// Deprecated: No replacement specified.")

    def test_non_deprecated_field_unchanged(self):
        schema = {
            "type": "object",
            "properties": {
                "name": {
                    "type": "string",
                    "description": "The display name.",
                }
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 1,
            "Non-deprecated field should produce exactly one line")
        self.assertIn("// The display name.", lines[0],
            "Non-deprecated field should carry trailing description comment")

    def test_non_deprecated_field_no_description(self):
        schema = {
            "type": "object",
            "properties": {
                "count": {"type": "integer"}
            },
        }
        lines = self._struct_lines(schema)
        self.assertEqual(len(lines), 1)
        self.assertNotIn("//", lines[0])

    def test_mixed_struct_deprecated_and_normal(self):
        """Regression: other fields in the same struct are unaffected."""
        schema = {
            "type": "object",
            "required": ["id"],
            "properties": {
                "id": {"type": "string", "description": "Unique identifier."},
                "status": {
                    "type": "string",
                    "deprecated": True,
                    "description": "Use new_status instead.",
                },
                "new_status": {"type": "string", "description": "Current status."},
            },
        }
        lines = self._struct_lines(schema)
        # id: 1 line, status: 2 lines (comment + field), new_status: 1 line = 4 total
        self.assertEqual(len(lines), 4)
        deprecated_comment_idx = next(
            i for i, l in enumerate(lines) if "// Deprecated:" in l
        )
        self.assertIn("Status string", lines[deprecated_comment_idx + 1])


if __name__ == "__main__":
    unittest.main()
