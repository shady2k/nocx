"""When a settings control is really in the state the proof asked for (nocx-1ebrs.35.6).

The Linux proof drives the shipped AppImage through its real GTK/WebKit
accessibility tree, so every decision it makes is a decision about one reading
of that tree. Two of those readings are easy to get wrong, and both are decided
here, away from pyatspi and away from a display:

- A rail row can be *in* the tree without being *on screen*. Settings holds
  more rows than the 1024x768 window it opens in, and the rail scrolls, so its
  last rows report FOCUSABLE and VISIBLE while STATE_SHOWING is false. The
  proof waited for a control to be SHOWING, so the About row read as "not
  observed: visible control About". A person scrolls the rail; focusing a row
  is how the engine is asked to scroll to it (measured with the local
  WebKitGTK 2.52 host: focusing the About row moves it from below the rail's
  bottom edge into the rail and turns STATE_SHOWING on).
- A <select> exposes every one of its options as a menu item. A row named
  "Nightly" is in the tree whether or not nightly is the channel, so a node
  name is not a value. The value is the row the combo marks SELECTED.

test_controls.py checks these rules; it needs no display, no session bus, no
Atspi and no AppImage.
"""

from dataclasses import dataclass

# What Reveal asks the driver to do with the next reading of the rail.
ACTIVATE = "activate"
FOCUS = "focus"
WAIT = "wait"
REFUSE = "refuse"


def normalise(name):
    return " ".join((name or "").split()).lower()


@dataclass(frozen=True)
class Option:
    """One row of the update-channel combo, as the tree reports it."""

    name: str
    selected: bool
    showing: bool


@dataclass(frozen=True)
class Channel:
    """One reading of the update-channel combo.

    `expanded` is the engine's own report that the option list is open. It is
    carried because it is evidence, not because it decides anything: WebKitGTK
    does not set it for this select (measured on 2.52, with the list open and
    again after a choice landed), so waiting for it to turn off would wait for
    a bit the tree never flips.
    """

    options: tuple = ()
    expanded: bool = False

    def choice(self):
        """The option the combo marks selected, or None when that is ambiguous.

        No selected row means the value is not announced yet; more than one
        means the reading cannot be trusted as a value.
        """
        selected = [option for option in self.options if option.selected]
        if len(selected) != 1:
            return None
        return normalise(selected[0].name)

    def describe(self):
        if not self.options:
            return "the combo box lists no options"
        rows = []
        for option in self.options:
            row = "[" + option.name + "]" if option.selected else option.name
            if not option.showing:
                row += " (off screen)"
            rows.append(row)
        return " | ".join(rows)


def channel_settled(reading, wanted):
    """Whether the combo's own value is `wanted`.

    Rows are not values. Every option is in the tree at all times, and rows are
    SHOWING both for the closed combo and after a choice landed (measured), so
    "a node named Nightly exists" and "a node named Nightly is on screen" are
    both true before the channel was ever switched. Only `choice()` answers the
    question the proof is asking.
    """
    return reading.choice() == normalise(wanted)


@dataclass(frozen=True)
class Rail:
    """One look at the settings rail.

    `rows` is every row the rail holds, in tree order, whether or not it is on
    screen; `showing` is the sublist the engine reports as painted.
    """

    rows: tuple = ()
    showing: tuple = ()

    def describe(self):
        rows = ", ".join(self.rows) if self.rows else "nothing"
        on_screen = ", ".join(self.showing) if self.showing else "nothing"
        return "rail rows: " + rows + " | on screen: " + on_screen


class Reveal:
    """Deciding how to bring one rail row on screen, one reading at a time.

    Activating a row the engine does not consider shown is not what a person
    does, and waiting for the rail to scroll by itself waits forever. So:
    activate what is already on screen; otherwise ask the engine to focus the
    row, which is what Tab does and which scrolls the rail; and if the rail
    says it holds the row but focusing it twice did not put it on screen, stop
    and say so instead of retrying until some timeout.
    """

    def __init__(self, wanted, focus_attempts=2):
        self.wanted = normalise(wanted)
        self.focus_attempts = focus_attempts
        self.focuses = 0

    def step(self, rail):
        if self.wanted in [normalise(name) for name in rail.showing]:
            return ACTIVATE
        if self.wanted not in [normalise(name) for name in rail.rows]:
            # Not rendered yet (Settings may still be opening): wait, and let
            # the caller's deadline decide when waiting has failed.
            return WAIT
        if self.focuses < self.focus_attempts:
            self.focuses += 1
            return FOCUS
        return REFUSE

    def refusal(self, rail):
        return ("the settings rail holds " + self.wanted
                + " but focusing it did not put it on screen; " + rail.describe())
