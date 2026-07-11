# Deep Thoughts — voice & style guide

The creative prompt for generating a Jack Handey–style "Deep Thought." This file is the silo's source
of truth for the *voice*; it does not depend on the user-level `deep-thoughts` skill being installed —
everything needed is here. The *when*, the output **path**, and the **frontmatter** are operational
constraints owned by [CLAUDE.md](../CLAUDE.md) (Automatic session behavior); this file owns only how to
write the thought.

## Voice & Style Guide

"Deep Thoughts by Jack Handey" is a recurring SNL segment. Reproduce this exact register:

- **Deadpan earnestness** — the narrator is never in on the joke; play it completely straight.
- **Narrator as subject** — the narrator is the butt of the joke; never punch outward at others.
- **Brevity** — 1 to 4 sentences maximum; the shorter, the better.
- **No winking** — no "haha", no "just kidding", no meta-commentary, no emoji.
- **Mock-philosophical gravity** — sound like you're imparting wisdom even as you reveal something
  uncomfortable.

**The core mechanism: comedic bait-and-switch.** Every Deep Thought runs on a strict contrast between
a gentle, poetic **setup** and a completely unhinged, coldly logical, or nakedly selfish
**conclusion**. The softer and more sincere the opening, the harder the turn lands. If the setup and
the payoff feel like they belong to the same sentence, you haven't turned hard enough.

**The three-part structure:**

1. **The Gentle Setup** — open on a cliché, a New Age observation, a childhood memory, or a classic
   philosophical premise. Use soft imagery: butterflies, grandparents, wishes, rivers, the stars.
2. **The Turn** — a sudden pivot that derails the pleasant thought, triggered by a mundane reality, a
   bizarre choice, or a dark twist. This is the hinge; it should feel abrupt, not eased into.
3. **The Deadpan Wrap-up** — a matter-of-fact conclusion that treats the absurdity as perfectly
   reasonable, as if it obviously followed from the setup.

**The persona (embody, don't describe):**

- **Unearned confidence** — he genuinely believes these thoughts are profound and helpful to humanity.
- **Casual cruelty** — he says terrible, selfish, or sociopathic things with total innocence and no
  malice; he does not notice they are terrible.
- **Childlike logic** — he resolves complex emotional or philosophical problems with absurd, literal,
  caveman-simple reasoning, and is satisfied by it.

Reference register (do not reuse these, just calibrate to them):

> "If you ever drop your keys into a river of molten lava, let 'em go, because man, they're gone."

> "Before you criticize someone, you should walk a mile in their shoes. That way, when you criticize
> them, you're a mile away and you have their shoes."

> "I hope if dogs ever take over the world and they choose a king, they don't just go by size,
> because I bet there are some Chihuahuas with some good ideas."

## Session Reflection (do this first, every time)

Before generating the thought, scan the current conversation for raw material:

1. **Identify notable events** — tasks completed, files created or edited, bugs encountered, tools
   used, decisions made, creative choices, recurring themes, moments of friction or triumph.
2. **Extract 2–3 concrete details** — be specific (e.g., "fixed frontmatter in three song files",
   "chose Phrygian Dominant over Dorian", "the lint script found 7 orphaned links").
3. **Distill a topic** — name the most prominent session thread in 1–3 words.
4. **Ground the thought** — the absurdist pivot should feel earned by the actual session events, not
   generic. The narrator is reflecting on something they literally just did or experienced.

The result should feel like: *"I just spent 40 minutes on this, and somehow the universe has
something to say about it."*

## Output format

Write the thought itself as a markdown **blockquote**, below the OKF frontmatter that
[CLAUDE.md](../CLAUDE.md) specifies. Then display the thought to the user in your response, followed by
the file path where it was saved. (The exact directory, filename pattern, and frontmatter fields are
defined operationally in CLAUDE.md — this file governs only the wording.)

One frontmatter field is load-bearing for the voice to survive: the required **`description`**. It holds
a dry, literal one-sentence summary of the session event — the same grounding you extract in "Session
Reflection" above, written completely straight (no joke, no imagery). This is the *only* text the search
index embeds; the comedic blockquote is kept for humans but never indexed. So the reflection you do to
earn the joke also becomes the `description` — write it there plainly, then let the blockquote be as
unhinged as it needs to be.

## Quality Check (before writing the file)

1. **Gentle Setup** — does it open on a soft, sincere, or poetic premise?
2. **The Turn** — is there a sharp pivot to something absurd, dark, or self-incriminating (not an
   easing-in, but a hinge)?
3. **Deadpan Wrap-up** — does it land the absurdity as if it were perfectly reasonable?
4. **Contrast** — is the gap between setup and conclusion wide? (If they feel like the same thought,
   turn harder.)
5. **Persona** — does the narrator show unearned confidence, casual cruelty, or childlike logic — and
   never notice?
6. Is it deadpan — no humor cues, no winking, no emoji?
7. Is it 1–4 sentences?
8. Is the narrator the subject of the joke, never punching outward?
9. Is it visibly connected to something that actually happened this session?

If any check fails, redraft before proceeding.
