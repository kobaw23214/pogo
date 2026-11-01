//! Content pacing for managed software outputs, independent of callback servicing.

use std::time::{Duration, Instant};

#[derive(Debug)]
pub struct ContentPace {
    output_id: u32,
    last_content: Option<Instant>,
    service_had_content: bool,
    pending_window: Option<u32>,
}

impl ContentPace {
    pub fn new(output_id: u32) -> Self {
        Self {
            output_id,
            last_content: None,
            service_had_content: false,
            pending_window: None,
        }
    }

    pub fn committed(&mut self, output_id: u32, window_id: u32, buffer_damage: bool) {
        if output_id == self.output_id && buffer_damage {
            self.pending_window = Some(window_id);
        }
    }

    pub fn pending(&self) -> bool {
        self.pending_window.is_some()
    }

    pub fn retain_window(&mut self, visible_window: Option<u32>) {
        if self.pending_window != visible_window {
            self.pending_window = None;
        }
    }

    fn content_due(&self, period: Duration, now: Instant) -> bool {
        self.last_content
            .is_none_or(|last| now.saturating_duration_since(last) >= period)
    }

    pub fn due(&self, service_due: bool, period: Duration, now: Instant) -> bool {
        self.content_due(period, now)
            && (service_due || (self.pending() && !self.service_had_content))
    }

    pub fn serviced(&mut self, at: Instant, published_damage: bool) {
        let fresh_content = published_damage && (self.pending() || self.last_content.is_none());
        self.service_had_content = fresh_content;
        self.pending_window = None;
        if fresh_content {
            self.last_content = Some(at);
        }
    }

    pub fn next_due(&self, service_due: Instant, period: Duration, now: Instant) -> Instant {
        let content_due = self.last_content.map_or(now, |last| last + period);
        let normal = service_due.max(content_due);
        if self.pending() && !self.service_had_content {
            normal.min(content_due.max(now))
        } else {
            normal
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const PERIOD: Duration = Duration::from_nanos(16_666_667);

    #[test]
    fn empty_service_does_not_delay_a_real_commit() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        pace.serviced(start, true);
        pace.serviced(start + PERIOD, false);
        let commit = start + PERIOD + Duration::from_millis(1);
        pace.committed(7, 91, true);
        pace.retain_window(Some(91));
        assert!(pace.due(false, PERIOD, commit));
        assert_eq!(pace.next_due(start + PERIOD * 2, PERIOD, commit), commit);
    }

    #[test]
    fn buffer_age_catch_up_is_not_a_new_scene() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        pace.serviced(start, true);
        pace.serviced(start + PERIOD, true);
        let commit = start + PERIOD + Duration::from_millis(1);
        pace.committed(7, 91, true);
        assert!(pace.due(false, PERIOD, commit));
    }

    #[test]
    fn held_frames_consume_no_content_budget() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        pace.committed(7, 91, true);
        pace.serviced(start, false);
        pace.committed(7, 91, true);
        assert!(pace.due(false, PERIOD, start + Duration::from_millis(1)));
    }

    #[test]
    fn waiting_for_a_pool_slot_keeps_one_pending_commit() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        for _ in 0..1000 {
            pace.committed(7, 91, true);
            pace.retain_window(Some(91));
            assert!(pace.due(false, PERIOD, start));
        }
        assert!(pace.pending());
        pace.serviced(start, true);
        assert!(!pace.pending());
        assert!(!pace.due(true, PERIOD, start + PERIOD / 2));
    }

    #[test]
    fn metadata_and_other_output_commits_cannot_wake() {
        let now = Instant::now();
        let mut pace = ContentPace::new(7);
        pace.committed(7, 91, false);
        assert!(!pace.pending());
        pace.committed(8, 91, true);
        assert!(!pace.pending());
        assert!(!pace.due(false, PERIOD, now));
    }

    #[test]
    fn closed_parked_or_rebound_window_cancels_the_commit() {
        for current in [None, Some(92)] {
            let mut pace = ContentPace::new(7);
            pace.committed(7, 91, true);
            pace.retain_window(current);
            assert!(!pace.pending());
            assert!(!pace.due(false, PERIOD, Instant::now()));
        }
    }

    #[test]
    fn a_restarted_capture_inherits_no_pending_commit_or_content_clock() {
        let start = Instant::now();
        let mut old = ContentPace::new(7);
        old.serviced(start, true);
        old.committed(7, 91, true);
        drop(old);
        let fresh = ContentPace::new(7);
        assert!(!fresh.pending());
        assert!(fresh.due(true, PERIOD, start));
    }

    #[test]
    fn fresh_content_never_borrows_from_the_next_period() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        pace.serviced(start, true);
        pace.serviced(start + PERIOD / 2, false);
        pace.committed(7, 91, true);
        assert!(!pace.due(true, PERIOD, start + PERIOD - Duration::from_nanos(1)));
        assert_eq!(
            pace.next_due(start + PERIOD / 2, PERIOD, start),
            start + PERIOD
        );
        assert!(pace.due(false, PERIOD, start + PERIOD));
    }

    #[test]
    fn idle_services_keep_their_deadline_and_do_not_spin() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        for n in 0..600 {
            let now = start + PERIOD * n;
            assert!(pace.due(true, PERIOD, now));
            pace.serviced(now, false);
            assert!(!pace.due(false, PERIOD, now));
            assert_eq!(pace.next_due(now + PERIOD, PERIOD, now), now + PERIOD);
        }
    }

    #[test]
    fn commit_storm_is_latest_only_and_content_is_capped_at_sixty_hz() {
        let start = Instant::now();
        let mut pace = ContentPace::new(7);
        let mut published = Vec::new();
        for us in (0..2_000_000).step_by(100) {
            let now = start + Duration::from_micros(us);
            pace.committed(7, 91, true);
            pace.retain_window(Some(91));
            if pace.due(true, PERIOD, now) {
                pace.serviced(now, true);
                published.push(now);
            }
        }
        assert!(published.len() <= 120);
        assert!(
            published
                .windows(2)
                .all(|p| p[1].duration_since(p[0]) >= PERIOD)
        );
    }

    #[test]
    fn independent_outputs_have_independent_content_and_service_clocks() {
        let start = Instant::now();
        let mut first = ContentPace::new(7);
        let mut second = ContentPace::new(8);
        first.serviced(start, true);
        second.serviced(start, false);
        second.committed(8, 92, true);
        assert!(!first.due(true, PERIOD, start + PERIOD / 2));
        assert!(second.due(false, PERIOD, start + PERIOD / 2));
    }
}
