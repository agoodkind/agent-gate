create trigger fail_evaluation_layer_detail
		before insert on gate_evaluation_layers
		begin select raise(abort, 'forced evaluation detail failure'); end
