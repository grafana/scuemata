package exemplars

import "github.com/grafana/thema"

defaultchange: {
	description: "The default value for a field is changed, entailing a new sequence."
	l:           thema.#Lineage & {
		schemas: [{
			version: [0, 0]
			schema: aunion: *"foo" | "bar" | "baz"
		}, {
			version: [1, 0]
			schema: aunion: "foo" | *"bar" | "baz"
		}]
		lenses: [{
			to: [0, 0]
			from: [1, 0]
			input: _
			result: {
				if input.aunion == "bar" {
					aunion: "foo"
				}
				if input.aunion != "bar" {
					aunion: input.anion
				}
			}
			lacunas: [
				if input.aunion == "foo" {
					thema.#Lacuna & {
						sourceFields: [{
							path:  "aunion"
							value: input.aunion
						}]
						targetFields: [{
							path:  "aunion"
							value: result.aunion
						}]
					}
					message: "aunion was the source default, \"bar\", and was changed to the target default, \"foo\""
					type:    thema.#LacunaTypes.ChangedDefault
				},
			]
		}, {
			to: [1, 0]
			from: [0, 0]
			input: _
			result: {
				// FIXME lenses need more structure to allow disambiguating absence and presence in the instance
				if input.aunion == "foo" {
					aunion: "bar"
				}
				if input.aunion != "foo" {
					aunion: input.anion
				}
			}
			lacunas: [
				// FIXME really feels like these lacunas should be able to be autogenerated, at least for simple cases?
				if input.aunion == "foo" {
					thema.#Lacuna & {
						sourceFields: [{
							path:  "aunion"
							value: input.aunion
						}]
						targetFields: [{
							path:  "aunion"
							value: result.aunion
						}]
					}
					message: "aunion was the source default, \"foo\", and was changed to the target default, \"bar\""
					type:    thema.#LacunaTypes.ChangedDefault
				},
			]
		}]
	}
}
