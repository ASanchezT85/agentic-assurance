# Motion

Motion communicates state change, not futurism.

- hover/focus: 100-150 ms;
- panel/drawer: 160-220 ms;
- live pulse: only for genuinely live/connected state, 2 s+ subtle cycle;
- incident severity never flashes;
- graph updates interpolate only when doing so does not hide jumps;
- respect `prefers-reduced-motion` and remove nonessential animation.

Never animate raw evidence chronology in a way that changes perceived order.
