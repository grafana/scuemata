# Thema

**_This repo is in a pre-alpha preview state. Essential logic is mostly settled. Helpers, docs and tutorials are Coming Soon™._**

Thema is a system for writing schemas. Like JSON Schema or OpenAPI, it is general-purpose, and most obviously useful as an [IDL](https://en.wikipedia.org/wiki/Interface_description_language).

Unlike JSON Schema, OpenAPI, or any other extant schema system, Thema's chief focus is on the _evolution_ of schema. Rather than "one file/logical structure, one schema," Thema is "one file/logical structure, all the schema for a given kind of object, and logic for translating between them."

The effect of encapsulating schema definition, evolution, and translation into a single, portable, machine-verifiable logical structure is transformative. Taken together, these pieces allow systems that rely on schemas as the contracts for their communication to decouple and evolve independently - even across breaking changes to those schema.

Learn more in our (TODO) [docs](TODO), or in this [overview video](https://www.youtube.com/watch?v=PpoS_ThntEM)!

## Maturity

Thema is in early adolescence: it's mostly formed, but there are still some crucial undeveloped parts. Specifically, there are two planned changes that we are almost certain will cause breakages for users of thema:

* [Reverse translation within sequences](TODO)
* [Object headers](TODO)

Once these changes are finalized, however, we aim to treat the CUE and Go APIs as stable, scrupulously avoiding any breaking changes.
