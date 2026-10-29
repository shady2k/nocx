#!/usr/bin/env python3
"""Unit checks for reading the settings controls, not proof of a shipped update.

These decide what a single reading of the accessibility tree means: whether the
update channel is really nightly, and whether a rail row is on screen or only
in the tree. They need no display, no session bus, no Atspi and no AppImage, so
they run on any runner — including the one that has nothing to install.

The last test in each group replays the reading that failed the published
AppImage proof (nocx-1ebrs.35.6) and would have failed the old rules.
"""

import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "update_proof_controls", Path(__file__).with_name("controls.py"))
controls = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(controls)


CHANNEL = controls.Channel
OPTION = controls.Option
RAIL = controls.Rail


class ChannelTests(unittest.TestCase):
    def test_the_selected_option_is_the_value(self):
        reading = CHANNEL((OPTION("Stable", selected=False, showing=True),
                           OPTION("Nightly", selected=True, showing=True)))
        self.assertEqual(reading.choice(), "nightly")
        self.assertTrue(controls.channel_settled(reading, "nightly"))

    def test_a_listed_option_is_not_the_value(self):
        # A closed <select> exposes every option as a menu item: "Nightly" is
        # in the tree, and showing, while the channel is still Stable.
        reading = CHANNEL((OPTION("Stable", selected=True, showing=True),
                           OPTION("Nightly", selected=False, showing=True)))
        self.assertEqual(reading.choice(), "stable")
        self.assertFalse(controls.channel_settled(reading, "nightly"))

    def test_rows_keep_showing_after_the_choice_lands(self):
        # Measured on WebKitGTK 2.52: the previously selected row stays
        # SHOWING, so showing cannot decide the value either way.
        reading = CHANNEL((OPTION("Stable", selected=False, showing=True),
                           OPTION("Nightly", selected=True, showing=True)))
        self.assertTrue(controls.channel_settled(reading, "nightly"))

    def test_an_unannounced_value_is_not_a_choice(self):
        reading = CHANNEL((OPTION("Stable", selected=False, showing=True),
                           OPTION("Nightly", selected=False, showing=False)))
        self.assertIsNone(reading.choice())
        self.assertFalse(controls.channel_settled(reading, "nightly"))

    def test_two_selected_rows_are_not_a_choice(self):
        reading = CHANNEL((OPTION("Stable", selected=True, showing=True),
                           OPTION("Nightly", selected=True, showing=True)))
        self.assertIsNone(reading.choice())
        self.assertFalse(controls.channel_settled(reading, "nightly"))

    def test_no_options_yet_is_not_a_choice(self):
        self.assertEqual(CHANNEL(), CHANNEL(options=(), expanded=False))
        self.assertIsNone(CHANNEL().choice())
        self.assertFalse(controls.channel_settled(CHANNEL(), "nightly"))

    def test_names_are_read_without_layout_whitespace_or_case(self):
        reading = CHANNEL((OPTION("  nightly\n", selected=True, showing=True),))
        self.assertTrue(controls.channel_settled(reading, " Nightly "))

    def test_an_open_list_does_not_change_what_the_value_is(self):
        # The engine's own "expanded" bit is kept as evidence only: WebKitGTK
        # never sets it for this select, so nothing may wait on it.
        reading = CHANNEL((OPTION("Stable", selected=False, showing=True),
                           OPTION("Nightly", selected=True, showing=True)), expanded=True)
        self.assertTrue(controls.channel_settled(reading, "nightly"))

    def test_the_reading_that_failed_the_proof_is_not_a_switched_channel(self):
        # From transfer/r7/proof-linux.json, after the channel was pressed:
        # both rows showing, Stable still the selected one, no popup reported.
        reading = CHANNEL((OPTION("Stable", selected=True, showing=True),
                           OPTION("Nightly", selected=False, showing=True)))
        self.assertEqual(
            reading.describe(), "[Stable] | Nightly")
        self.assertFalse(controls.channel_settled(reading, "nightly"))


class RevealTests(unittest.TestCase):
    def test_a_row_on_screen_is_activated(self):
        reveal = controls.Reveal("About")
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates", "About"))),
                         controls.ACTIVATE)
        self.assertEqual(reveal.focuses, 0)

    def test_a_row_below_the_fold_is_focused_first(self):
        reveal = controls.Reveal("About")
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates",))), controls.FOCUS)
        self.assertEqual(reveal.focuses, 1)

    def test_a_row_is_activated_once_focusing_put_it_on_screen(self):
        reveal = controls.Reveal("About")
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates",))), controls.FOCUS)
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates", "About"))),
                         controls.ACTIVATE)

    def test_a_row_the_rail_never_shows_is_refused(self):
        reveal = controls.Reveal("About")
        rail = RAIL(("Updates", "About"), ("Updates",))
        self.assertEqual([reveal.step(rail), reveal.step(rail), reveal.step(rail)],
                         [controls.FOCUS, controls.FOCUS, controls.REFUSE])
        self.assertIn("holds about", reveal.refusal(rail))
        self.assertIn("on screen: Updates", reveal.refusal(rail))

    def test_a_row_the_rail_does_not_hold_yet_is_waited_for(self):
        reveal = controls.Reveal("About")
        empty = RAIL(("Updates",), ("Updates",))
        self.assertEqual([reveal.step(empty), reveal.step(empty)], [controls.WAIT, controls.WAIT])
        self.assertEqual(reveal.focuses, 0)
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates",))), controls.FOCUS)

    def test_row_names_are_matched_without_layout_whitespace_or_case(self):
        reveal = controls.Reveal(" About ")
        self.assertEqual(reveal.step(RAIL(("Updates", "About"), ("Updates", " about"))),
                         controls.ACTIVATE)

    def test_the_reading_that_failed_the_proof_needs_a_focus(self):
        # The About row was in the rail and not on screen: the old harness
        # waited for a shown control and timed out instead of scrolling.
        rail = RAIL(("Updates", "History", "About"), ("Updates", "History"))
        self.assertEqual(controls.Reveal("About").step(rail), controls.FOCUS)


if __name__ == "__main__":
    unittest.main()
