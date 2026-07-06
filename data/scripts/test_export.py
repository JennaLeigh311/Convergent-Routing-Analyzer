#!/usr/bin/env python3
"""test_export.py — conformance tests for the §2 edge_attributes exporter.

Phase 8, issue #120. The frozen §2 derivation tables (docs/contracts.md §2) live in
export_edge_attributes.py as a hand-copied second source of truth; this test is the
mechanical guard that the copy stays equal to the contract, so a future edit can't
silently retune a default or the split rule and drift the headline numbers.

It drives the REAL derivation entry point (build_features + the helpers) with
synthetic pgRouting rows + raw OSM tag dicts that reproduce every case in the
language-neutral golden fixture docs/fixtures/edge_attributes/example_export.json,
and asserts the geometry-independent derived columns (segment_id, highway_class,
lanes_effective, maxspeed_kmh, capacity_vph) match. length_m/freeflow_time_s are
geometry-derived (the fixture uses authored round lengths), so we assert only their
internal consistency, not the fixture's authored length.

Pure stdlib (unittest) — no psycopg2, Docker, Postgres or Overpass. The exporter
imports psycopg2 lazily in main() precisely so this file can import it. Run:

    python3 -m unittest data/scripts/test_export.py     # from repo root
    cd data/scripts && python3 -m unittest test_export  # or from here
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import export_edge_attributes as ex  # noqa: E402

# A valid 2-point geometry; the derived columns under test don't depend on it.
COORDS = [[-8.6100000, 41.1500000], [-8.5900000, 41.1510000]]

# Each case mirrors one fixture way: (osm_id, tags, [(dir, lanes, maxspeed, capacity_vph)]).
# oneway=yes is set where the fixture's lanes_effective / single F row imply one-way
# (e.g. 60500777 keeps bare lanes=4 whole -> must be one-way).
FIXTURE_CASES = [
    (27583001, {"highway": "primary", "oneway": "yes"}, [("F", 2, 60.0, 2880.0)]),
    (48800123, {"highway": "secondary"}, [("F", 2, 50.0, 2520.0), ("R", 2, 50.0, 2520.0)]),
    (9876543, {"highway": "residential", "oneway": "yes"}, [("F", 1, 30.0, 900.0)]),
    (33112200, {"highway": "trunk"}, [("F", 2, 80.0, 3240.0), ("R", 2, 80.0, 3240.0)]),
    (71200, {"highway": "service"}, [("F", 1, 20.0, 720.0), ("R", 1, 20.0, 720.0)]),
    (60500777, {"highway": "primary", "lanes": "4", "maxspeed": "80", "oneway": "yes"},
     [("F", 4, 80.0, 5760.0)]),
    (5051000, {"highway": "tertiary"}, [("F", 1, 40.0, 1080.0), ("R", 1, 40.0, 1080.0)]),
    (8123456, {"highway": "secondary", "lanes": "6"},
     [("F", 3, 50.0, 3780.0), ("R", 3, 50.0, 3780.0)]),
]

ALL_PROPS = {
    "segment_id", "edge_id", "source_node", "target_node", "osm_way_id",
    "highway_class", "lanes_effective", "length_m", "maxspeed_kmh",
    "freeflow_time_s", "capacity_vph",
}


def props_by_segment(ways_rows, tags_by_way):
    feats, _ = ex.build_features(ways_rows, tags_by_way)
    return {f["properties"]["segment_id"]: f["properties"] for f in feats}


class FixtureConformance(unittest.TestCase):
    def test_derivations_match_golden_fixture(self):
        for osm_id, tags, expected in FIXTURE_CASES:
            rows = [(osm_id, 1, 2, [c[:] for c in COORDS])]
            got = props_by_segment(rows, {osm_id: tags})
            self.assertEqual(len(got), len(expected),
                             f"way {osm_id}: emitted {sorted(got)} != {len(expected)} rows")
            base_class = ex.base_highway_class(tags["highway"])
            for direction, lanes, speed, cap in expected:
                seg = f"{osm_id}:0:{direction}"
                self.assertIn(seg, got, f"missing {seg}")
                p = got[seg]
                self.assertEqual(set(p), ALL_PROPS, f"{seg}: property set")
                self.assertEqual(p["highway_class"], base_class, seg)
                self.assertEqual(p["lanes_effective"], lanes, seg)
                self.assertEqual(p["maxspeed_kmh"], speed, seg)
                self.assertEqual(p["capacity_vph"], cap, seg)
                self.assertEqual(p["osm_way_id"], osm_id, seg)
                # freeflow is internally consistent with its own length/speed.
                self.assertAlmostEqual(
                    p["freeflow_time_s"], p["length_m"] / (p["maxspeed_kmh"] / 3.6),
                    places=9, msg=seg)
                self.assertGreater(p["length_m"], 0, seg)

    def test_multi_subsegment_seq(self):
        # Motorway way 905512: tagged lanes=3/maxspeed=100, one-way by convention,
        # two pgRouting sub-segments -> seq 0 and 1, forward only.
        tags = {905512: {"highway": "motorway", "lanes": "3", "maxspeed": "100"}}
        rows = [
            (905512, 20, 21, [[-8.60, 41.74], [-8.59, 41.742]]),
            (905512, 21, 22, [[-8.59, 41.742], [-8.58, 41.744]]),
        ]
        got = props_by_segment(rows, tags)
        self.assertEqual(set(got), {"905512:0:F", "905512:1:F"})
        for seg in got:
            self.assertEqual(got[seg]["lanes_effective"], 3)
            self.assertEqual(got[seg]["maxspeed_kmh"], 100.0)
            self.assertEqual(got[seg]["capacity_vph"], 5400.0)

    def test_fr_symmetry(self):
        # Two-way bare-lanes=6 secondary: F and R share way+seq, swap nodes, reverse
        # geometry, and carry the IDENTICAL length_m (reused, not re-summed).
        got = props_by_segment(
            [(8123456, 80, 81, [c[:] for c in COORDS])],
            {8123456: {"highway": "secondary", "lanes": "6"}})
        f, r = got["8123456:0:F"], got["8123456:0:R"]
        self.assertEqual((f["source_node"], f["target_node"]), (80, 81))
        self.assertEqual((r["source_node"], r["target_node"]), (81, 80))
        self.assertEqual(f["length_m"], r["length_m"])
        self.assertEqual(f["capacity_vph"], r["capacity_vph"])


class SeqContiguity(unittest.TestCase):
    def test_skipped_selfloop_does_not_gap_seq(self):
        # Middle sub-segment is a self-loop (source==target) -> skipped as
        # degenerate. The survivors must be seq 0 and 1 (contiguous), NOT 0 and 2.
        rows = [
            (555, 1, 2, [[-8.60, 41.15], [-8.599, 41.151]]),
            (555, 3, 3, [[-8.599, 41.151], [-8.598, 41.152]]),  # self-loop -> dropped
            (555, 4, 5, [[-8.598, 41.152], [-8.597, 41.153]]),
        ]
        got = props_by_segment(rows, {555: {"highway": "residential", "oneway": "yes"}})
        self.assertEqual(set(got), {"555:0:F", "555:1:F"})


class LanesPrecedence(unittest.TestCase):
    def test_direction_specific_tag_wins(self):
        self.assertEqual(ex.lanes_effective({"lanes:forward": "3", "lanes": "6"},
                                            "F", False, "secondary"), 3)
        self.assertEqual(ex.lanes_effective({"lanes:backward": "1", "lanes": "6"},
                                            "R", False, "secondary"), 1)

    def test_bare_lanes_oneway_used_whole(self):
        self.assertEqual(ex.lanes_effective({"lanes": "4"}, "F", True, "primary"), 4)

    def test_bare_lanes_twoway_is_split_and_clamped(self):
        self.assertEqual(ex.lanes_effective({"lanes": "6"}, "F", False, "secondary"), 3)
        # floor(1/2)=0 clamps up to the minimum of 1.
        self.assertEqual(ex.lanes_effective({"lanes": "1"}, "F", False, "secondary"), 1)

    def test_falls_back_to_class_default(self):
        self.assertEqual(ex.lanes_effective({}, "F", False, "tertiary"), 1)
        self.assertEqual(ex.lanes_effective({}, "F", False, "motorway"), 3)


class MaxspeedRule(unittest.TestCase):
    def test_directional_maxspeed_tag_is_ignored(self):
        # §2 has NO maxspeed:forward/backward tier: bare `maxspeed` wins for BOTH
        # directions. Regression guard for the directional-tier removal.
        tags = {"maxspeed": "50", "maxspeed:forward": "70"}
        self.assertEqual(ex.maxspeed_kmh(tags, "primary"), 50.0)

    def test_tagged_then_default(self):
        self.assertEqual(ex.maxspeed_kmh({"maxspeed": "80"}, "primary"), 80.0)
        self.assertEqual(ex.maxspeed_kmh({}, "primary"), 60.0)

    def test_mph_conversion_and_implicit_zones(self):
        self.assertAlmostEqual(ex.parse_maxspeed_kmh("30 mph"), 30 * 1.609344, places=6)
        self.assertIsNone(ex.parse_maxspeed_kmh("none"))
        self.assertIsNone(ex.parse_maxspeed_kmh("ro:urban"))


class Directions(unittest.TestCase):
    def test_oneway_variants(self):
        self.assertEqual(ex.directions({"oneway": "yes"}, "primary"), (True, False))
        self.assertEqual(ex.directions({"oneway": "-1"}, "primary"), (False, True))
        self.assertEqual(ex.directions({"oneway": "no"}, "primary"), (True, True))

    def test_implicit_oneway_by_class_and_junction(self):
        self.assertEqual(ex.directions({}, "motorway"), (True, False))
        self.assertEqual(ex.directions({"junction": "roundabout"}, "residential"),
                         (True, False))
        self.assertEqual(ex.directions({}, "residential"), (True, True))


class MessyIntParsing(unittest.TestCase):
    def test_leading_int_and_rejects(self):
        self.assertEqual(ex.parse_int("2;3"), 2)
        self.assertEqual(ex.parse_int("1.5"), 1)
        self.assertIsNone(ex.parse_int("0"))
        self.assertIsNone(ex.parse_int("-1"))
        self.assertIsNone(ex.parse_int(None))


if __name__ == "__main__":
    unittest.main()
