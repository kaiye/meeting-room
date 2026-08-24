# Meeting custom prompt design

Each meeting keeps one optional custom prompt alongside its selected summary
template. The meeting page exposes it through an edit icon adjacent to the
template selector. The user can save or clear it; loading the meeting restores
it. Summary generation sends the selected template and this prompt together,
so the prompt is appended after the template instructions and does not modify
the shared template itself.
